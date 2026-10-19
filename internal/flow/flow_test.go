package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"devflow/internal/ai"
)

type scriptedModel struct {
	calls                    []string
	implementations, reviews int
	ask                      bool
}

func (m *scriptedModel) Complete(_ context.Context, r ai.Request) (ai.Result, error) {
	m.calls = append(m.calls, r.Stage)
	d := Document{Report: "Prepared " + r.Stage}
	switch r.Stage {
	case "clarify":
		d.Criteria = []Criterion{{ID: "AC-1", Text: "Greet names up to ten Unicode characters; empty defaults to world; reject longer names"}}
		d.Questions = []string{}
		if m.ask && strings.Contains(r.Input, `"answers":null`) {
			d.Questions = []string{"What is the name limit?"}
		}
	case "test_cases":
		d.Cases = fixtureCases()
	case "specification":
		d.Design = "Use UTF-8 rune count and explicit error; preserve main"
		d.PlannedFiles = []string{"main.go", "main_test.go"}
	case "implementation":
		m.implementations++
		main := `package main
import("fmt";"unicode/utf8")
func greet(name string)(string,error){if utf8.RuneCountInString(name)>10{return "",fmt.Errorf("too long")};if name==""{name="world"};return "Hello, "+name+"!",nil}
func main(){}`
		tests := `package main
import "testing"
func TestDefault(t *testing.T){s,e:=greet("");if e!=nil||s!="Hello, world!"{t.Fatalf("%q %v",s,e)}}
func TestTooLong(t *testing.T){if _,e:=greet("12345678901");e==nil{t.Fatal("must reject")}}
func TestBoundary(t *testing.T){s,e:=greet("1234567890");if e!=nil||s!="Hello, 1234567890!"{t.Fatalf("%q %v",s,e)}}`
		if m.implementations > 1 {
			b, e := format.Source([]byte(main))
			if e != nil {
				return ai.Result{}, e
			}
			main = string(b)
		}
		b, e := format.Source([]byte(tests))
		if e != nil {
			return ai.Result{}, e
		}
		d.Files = []File{{Path: "main.go", Content: main}, {Path: "main_test.go", Content: string(b)}}
	case "review":
		m.reviews++
		d.Findings = []Finding{}
		if m.reviews == 1 {
			d.Findings = []Finding{{ID: "R1", Severity: "medium", Path: "main_test.go", Detail: "Add an explicit Unicode boundary assertion"}}
		}
	}
	if r.Stage == "implementation" && m.implementations > 2 {
		d.Files[1].Content += "\nfunc TestUnicodeBoundary(t *testing.T) {\n\ts, e := greet(\"яяяяяяяяяя\")\n\tif e != nil || s != \"Hello, яяяяяяяяяя!\" {\n\t\tt.Fatalf(\"%q %v\", s, e)\n\t}\n}\n"
	}
	b, e := json.Marshal(d)
	if e != nil {
		return ai.Result{}, e
	}
	var raw map[string]any
	if e = json.Unmarshal(b, &raw); e != nil {
		return ai.Result{}, e
	}
	if r.Stage == "clarify" && raw["questions"] == nil {
		raw["questions"] = []string{}
	}
	if r.Stage == "review" && raw["findings"] == nil {
		raw["findings"] = []Finding{}
	}
	b, e = json.Marshal(raw)
	return ai.Result{ID: fmt.Sprintf("fake-%d", len(m.calls)), Output: b}, e
}
func fixtureCases() []Case {
	return []Case{{ID: "TC-1", Criterion: "AC-1", Kind: "positive", Steps: "empty name", Expected: "default greeting", TestFile: "main_test.go", TestName: "TestDefault"}, {ID: "TC-2", Criterion: "AC-1", Kind: "negative", Steps: "11 chars", Expected: "error", TestFile: "main_test.go", TestName: "TestTooLong"}, {ID: "TC-3", Criterion: "AC-1", Kind: "boundary", Steps: "10 chars", Expected: "greeting", TestFile: "main_test.go", TestName: "TestBoundary"}}
}
func fixture(t *testing.T) (context.Context, string, string) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	repo := filepath.Join(root, "target")
	if e := os.Mkdir(repo, 0755); e != nil {
		t.Fatal(e)
	}
	for n, s := range map[string]string{"go.mod": "module example.com/fixture\n\ngo 1.25.0\n", "main.go": "package main\n\nfunc main() {}\n"} {
		if e := os.WriteFile(filepath.Join(repo, n), []byte(s), 0644); e != nil {
			t.Fatal(e)
		}
	}
	for _, args := range [][]string{{"init", "-b", "main"}, {"add", "."}, {"-c", "user.name=Test", "-c", "user.email=test@localhost", "commit", "-m", "Initial"}} {
		if out, e := git(ctx, repo, args...); e != nil {
			t.Fatalf("git: %s: %v", out, e)
		}
	}
	dir, e := Start(ctx, StartOptions{Repo: repo, Task: "Add greeting", Dir: filepath.Join(root, "run"), Runner: "local", BuildPackage: "."})
	if e != nil {
		t.Fatal(e)
	}
	return ctx, dir, repo
}
func TestPipelineRepairsReviewAndRelease(t *testing.T) {
	ctx, dir, repo := fixture(t)
	m := &scriptedModel{}
	engine := Engine{Model: m}
	if e := engine.Resume(ctx, dir, ""); e != nil {
		t.Fatal(e)
	}
	r, e := Load(dir)
	if e != nil {
		t.Fatal(e)
	}
	if r.Status != "awaiting_release" || r.Repairs != 2 || m.implementations != 3 || m.reviews != 2 {
		t.Fatalf("bad lifecycle: status=%s repairs=%d implementations=%d reviews=%d", r.Status, r.Repairs, m.implementations, m.reviews)
	}
	if e = engine.Release(ctx, dir, "wrong", "v0.1.0"); e == nil {
		t.Fatal("unapproved SHA released")
	}
	if e = engine.Release(ctx, dir, r.ReviewedSHA, "v0.1.0"); e != nil {
		t.Fatal(e)
	}
	r, e = Load(dir)
	if e != nil {
		t.Fatal(e)
	}
	if r.Status != "released" {
		t.Fatal(r.Status)
	}
	if _, e = os.Stat(r.ReleaseURL); e != nil {
		t.Fatal(e)
	}
	if e = engine.Release(ctx, dir, r.ReviewedSHA, "v0.1.0"); e != nil {
		t.Fatal("release not idempotent:", e)
	}
	status, e := git(ctx, repo, "status", "--porcelain")
	if e != nil || status != "" {
		t.Fatal("original worktree changed")
	}
}
func TestClarificationAndArtifactTampering(t *testing.T) {
	ctx, dir, _ := fixture(t)
	m := &scriptedModel{ask: true}
	engine := Engine{Model: m}
	if e := engine.Resume(ctx, dir, ""); e != nil {
		t.Fatal(e)
	}
	r, _ := Load(dir)
	if r.Status != "awaiting_input" || r.Stage != 0 {
		t.Fatal("did not pause for questions")
	}
	if e := engine.Resume(ctx, dir, ""); e == nil {
		t.Fatal("missing answer accepted")
	}
	if e := engine.Resume(ctx, dir, "limit is ten"); e != nil {
		t.Fatal(e)
	}
	r, _ = Load(dir)
	path := filepath.Join(dir, r.Records[0].Artifact)
	if e := os.WriteFile(path, []byte("{}"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := engine.Release(ctx, dir, r.ReviewedSHA, "v1.0.0"); e == nil {
		t.Fatal("tampered artifact accepted")
	}
}
func TestFileTraversalAndSymlinkAreRejected(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if e := os.Symlink(outside, filepath.Join(root, "escape")); e != nil {
		t.Fatal(e)
	}
	p := Policy{MaxFiles: 20, MaxFileBytes: 1000}
	for _, path := range []string{"../evil.go", "/tmp/evil.go", "escape/evil.go", ".git/config", "go.mod"} {
		if e := applyFiles(root, []File{{Path: path, Content: "malicious"}}, []string{path}, p); e == nil {
			t.Fatalf("accepted %s", path)
		}
	}
	if _, e := os.Stat(filepath.Join(outside, "evil.go")); !os.IsNotExist(e) {
		t.Fatal("symlink escaped")
	}
}
func TestMissingExecutedCaseCannotPass(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/fixture\n"), 0600)
	os.WriteFile(filepath.Join(root, "main_test.go"), []byte("package main\nimport \"testing\"\nfunc TestDefault(t *testing.T) {}\n"), 0600)
	if e := verifyExecuted(root, fixtureCases()[:1], `{"Action":"pass","Package":"example.com/other","Test":"TestDefault"}`); e == nil {
		t.Fatal("unrelated package passed mapping")
	}
}
func TestLockExcludesConcurrentResume(t *testing.T) {
	dir := t.TempDir()
	unlock, e := lock(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer unlock()
	if u, e := lock(dir); e == nil {
		u()
		t.Fatal("concurrent lock accepted")
	}
}

func TestReviewMustExplicitlyReturnFindings(t *testing.T) {
	for _, b := range []string{`{"report":"looks good"}`, `{"report":"looks good","findings":null}`, `{"report":"looks good","findings":[],"approved":true}`} {
		if e := validateShape("review", []byte(b)); e == nil {
			t.Fatal("incomplete review accepted")
		}
	}
	if e := validateShape("review", []byte(`{"report":"looks good","findings":[]}`)); e != nil {
		t.Fatal(e)
	}
}
func TestRepairBudgetBlocks(t *testing.T) {
	dir := t.TempDir()
	r := Run{Status: "running"}
	p := Policy{MaxRepairs: 1}
	if e := repair(dir, &r, p, "first failure"); e != nil {
		t.Fatal(e)
	}
	if e := repair(dir, &r, p, "second failure"); e == nil || r.Status != "blocked" {
		t.Fatal("budget not enforced")
	}
}

// Optional: exercises the exact container restrictions used by the default runner.
func TestDockerChecks(t *testing.T) {
	if os.Getenv("DEVFLOW_TEST_DOCKER") != "1" {
		t.Skip("set DEVFLOW_TEST_DOCKER=1 with Docker and golang:1.25 available")
	}
	ctx, dir, _ := fixture(t)
	r, e := Load(dir)
	if e != nil {
		t.Fatal(e)
	}
	r.Runner = "docker"
	m := &scriptedModel{implementations: 1}
	result, e := m.Complete(ctx, ai.Request{Stage: "implementation"})
	if e != nil {
		t.Fatal(e)
	}
	doc, e := parseDocument(result.Output)
	if e != nil {
		t.Fatal(e)
	}
	p, e := policy(dir)
	if e != nil {
		t.Fatal(e)
	}
	if e = applyFiles(r.Worktree, doc.Files, []string{"main.go", "main_test.go"}, p); e != nil {
		t.Fatal(e)
	}
	logs, e := checks(ctx, r, p, fixtureCases())
	if e != nil {
		t.Fatalf("Docker checks: %v\n%s", e, logs)
	}
}
