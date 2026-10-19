package flow

import (
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

type boundedBuffer struct {
	bytes.Buffer
	Limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.Limit - b.Len()
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		b.Buffer.Write(p)
	}
	return n, nil
}
func command(ctx context.Context, dir string, timeout int, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	// Child processes never inherit the API key or GitHub tokens.
	for _, k := range []string{"PATH", "HOME", "TMPDIR", "GOCACHE", "GOPATH", "DOCKER_HOST", "DOCKER_CONTEXT"} {
		if v := os.Getenv(k); v != "" {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	if name == "gh" {
		for _, k := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
			if v := os.Getenv(k); v != "" {
				cmd.Env = append(cmd.Env, k+"="+v)
			}
		}
	}
	cmd.Env = append(cmd.Env, "GOWORK=off", "GOENV=off", "GOTOOLCHAIN=local", "CGO_ENABLED=0", "GIT_TERMINAL_PROMPT=0")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = time.Second
	b := &boundedBuffer{Limit: 2 << 20}
	cmd.Stdout = b
	cmd.Stderr = b
	e := cmd.Run()
	if ctx.Err() != nil {
		return b.String(), fmt.Errorf("%s timed out or was cancelled", name)
	}
	if e != nil {
		return b.String(), fmt.Errorf("%s failed: %w", name, e)
	}
	return b.String(), nil
}
func git(ctx context.Context, dir string, args ...string) (string, error) {
	args = append([]string{"-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false"}, args...)
	s, e := command(ctx, dir, 60, "git", args...)
	return strings.TrimSpace(s), e
}
func allowed(p string) bool {
	if !safeRelative(p) || !regexp.MustCompile(`^[A-Za-z0-9_/-]+(\.[A-Za-z0-9_-]+)?$`).MatchString(p) {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if strings.HasPrefix(part, ".") || part == "vendor" {
			return false
		}
	}
	return strings.HasSuffix(p, ".go") || p == "README.md"
}
func regularPath(root, relative string) error {
	if !safeRelative(relative) {
		return fmt.Errorf("unsafe path %q", relative)
	}
	current := root
	for _, part := range strings.Split(relative, "/") {
		current = filepath.Join(current, part)
		st, e := os.Lstat(current)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return e
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink forbidden: %s", relative)
		}
		if !st.IsDir() && !st.Mode().IsRegular() {
			return fmt.Errorf("special file forbidden: %s", relative)
		}
	}
	return nil
}
func repositoryContext(ctx context.Context, r Run, p Policy) (string, error) {
	names, e := git(ctx, r.Worktree, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if e != nil {
		return "", e
	}
	seen := map[string]bool{}
	var b strings.Builder
	for _, n := range strings.Split(names, "\x00") {
		if n == "" || seen[n] || !(allowed(n) || n == "go.mod") {
			continue
		}
		seen[n] = true
		if e = regularPath(r.Worktree, n); e != nil {
			return "", e
		}
		st, e := os.Stat(filepath.Join(r.Worktree, n))
		if e != nil {
			return "", e
		}
		if st.Size() > int64(p.MaxFileBytes) {
			return "", fmt.Errorf("source exceeds file budget: %s", n)
		}
		data, e := os.ReadFile(filepath.Join(r.Worktree, n))
		if e != nil {
			return "", e
		}
		fmt.Fprintf(&b, "\n--- file: %s ---\n%s\n", n, data)
		if b.Len() > p.MaxContext {
			return "", fmt.Errorf("repository exceeds context budget; use a smaller target module")
		}
	}
	return b.String(), nil
}
func applyFiles(root string, files []File, planned []string, p Policy) error {
	if len(files) == 0 || len(files) > p.MaxFiles {
		return fmt.Errorf("invalid changed-file count")
	}
	plan := map[string]bool{}
	for _, n := range planned {
		plan[n] = true
	}
	seen := map[string]bool{}
	for _, f := range files {
		if !allowed(f.Path) || !plan[f.Path] || seen[f.Path] {
			return fmt.Errorf("file not allowed by specification: %s", f.Path)
		}
		seen[f.Path] = true
		if len(f.Content) > p.MaxFileBytes {
			return fmt.Errorf("file too large: %s", f.Path)
		}
		if e := regularPath(root, f.Path); e != nil {
			return e
		}
	}
	for _, f := range files {
		dst := filepath.Join(root, f.Path)
		if e := os.MkdirAll(filepath.Dir(dst), 0755); e != nil {
			return e
		}
		if e := atomicWrite(dst, []byte(f.Content)); e != nil {
			return e
		}
	}
	return nil
}
func runGo(ctx context.Context, r Run, p Policy, args ...string) (string, error) {
	if r.Runner == "local" {
		return command(ctx, r.Worktree, p.CommandTimeout, "go", args...)
	}
	dockerArgs := []string{"run", "--rm", "--network=none", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--pids-limit=256", "--memory=1g", "--cpus=2", "--read-only", "--tmpfs", "/tmp:rw,exec,size=512m", "-e", "GOCACHE=/tmp/go-cache", "-e", "GOPATH=/tmp/go", "-e", "GOWORK=off", "-e", "GOTOOLCHAIN=local", "-e", "CGO_ENABLED=0", "-v", r.Worktree + ":/workspace", "-w", "/workspace", "golang:1.25", "go"}
	dockerArgs = append(dockerArgs, args...)
	return command(ctx, "", p.CommandTimeout, "docker", dockerArgs...)
}
func checks(ctx context.Context, r Run, p Policy, cases []Case) (string, error) {
	defer os.Remove(filepath.Join(r.Worktree, ".devflow-build"))
	var logs strings.Builder
	// gofmt -l must be empty; fixes are requested from the implementation skill.
	names, e := git(ctx, r.Worktree, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if e != nil {
		return "", e
	}
	seen := map[string]bool{}
	for _, n := range strings.Split(names, "\x00") {
		if !strings.HasSuffix(n, ".go") || seen[n] {
			continue
		}
		seen[n] = true
		if e = regularPath(r.Worktree, n); e != nil {
			return "", e
		}
		var s string
		if r.Runner == "local" {
			s, e = command(ctx, r.Worktree, p.CommandTimeout, "gofmt", "-l", n)
		} else {
			args := []string{"run", "--rm", "--network=none", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--read-only", "-v", r.Worktree + ":/workspace:ro", "-w", "/workspace", "golang:1.25", "gofmt", "-l", n}
			s, e = command(ctx, "", p.CommandTimeout, "docker", args...)
		}
		if e != nil || strings.TrimSpace(s) != "" {
			return "gofmt failed for " + n + "\n" + s, fmt.Errorf("gofmt check failed")
		}
	}
	logs.WriteString("gofmt: passed\n")
	for _, args := range [][]string{{"vet", "./..."}, {"test", "-json", "-count=1", "./..."}, {"build", "-o", filepath.Join(".devflow-build"), r.BuildPackage}} {
		s, err := runGo(ctx, r, p, args...)
		fmt.Fprintf(&logs, "$ go %s\n%s\n", strings.Join(args, " "), s)
		if err != nil {
			return logs.String(), err
		}
		if args[0] == "test" {
			if err = verifyExecuted(r.Worktree, cases, s); err != nil {
				return logs.String(), err
			}
		}
	}
	os.Remove(filepath.Join(r.Worktree, ".devflow-build"))
	return logs.String(), nil
}
func verifyExecuted(root string, cases []Case, output string) error {
	// Keys include package directory to prevent an unrelated same-named test from satisfying a case.
	passed := map[string]bool{}
	d := jsonDecoder(output)
	for {
		var event struct{ Action, Package, Test string }
		e := d.Decode(&event)
		if e == io.EOF {
			break
		}
		if e != nil {
			return fmt.Errorf("invalid go test JSON")
		}
		if event.Action == "pass" && event.Test != "" {
			passed[event.Package+"/"+event.Test] = true
		}
	}
	mod, e := os.ReadFile(filepath.Join(root, "go.mod"))
	if e != nil {
		return e
	}
	module := ""
	for _, l := range strings.Split(string(mod), "\n") {
		f := strings.Fields(l)
		if len(f) == 2 && f[0] == "module" {
			module = strings.Trim(f[1], "\"")
		}
	}
	for _, tc := range cases {
		if !allowed(tc.TestFile) || !strings.HasSuffix(tc.TestFile, "_test.go") {
			return fmt.Errorf("invalid test path")
		}
		if e = regularPath(root, tc.TestFile); e != nil {
			return e
		}
		f, e := parser.ParseFile(token.NewFileSet(), filepath.Join(root, tc.TestFile), nil, 0)
		if e != nil {
			return e
		}
		found := false
		for _, decl := range f.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == tc.TestName {
				found = true
			}
		}
		pkg := module
		if dir := filepath.ToSlash(filepath.Dir(tc.TestFile)); dir != "." {
			pkg += "/" + dir
		}
		if !found || !passed[pkg+"/"+tc.TestName] {
			return fmt.Errorf("case %s lacks an executed passing test %s:%s", tc.ID, tc.TestFile, tc.TestName)
		}
	}
	return nil
}
