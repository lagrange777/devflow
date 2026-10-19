package flow

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"devflow/internal/ai"
)

type Engine struct {
	Model ai.Model
	Log   func(string)
}
type StartOptions struct {
	Repo, Task, Dir, Rules, Runner, BuildPackage, GitHubRepo, BaseBranch string
	Publish                                                              bool
}

func (e Engine) log(s string) {
	if e.Log != nil {
		e.Log(s)
	}
}
func Start(ctx context.Context, o StartOptions) (string, error) {
	if strings.TrimSpace(o.Task) == "" {
		return "", fmt.Errorf("task is required")
	}
	if o.Runner != "local" && o.Runner != "docker" {
		return "", fmt.Errorf("runner must be docker or local")
	}
	if !regexp.MustCompile(`^(\.|\./[A-Za-z0-9_/-]+)$`).MatchString(o.BuildPackage) || strings.Contains(o.BuildPackage, "..") {
		return "", fmt.Errorf("invalid --package")
	}
	repo, err := filepath.Abs(o.Repo)
	if err != nil {
		return "", err
	}
	repo, err = filepath.EvalSymlinks(repo)
	if err != nil {
		return "", err
	}
	top, err := git(ctx, repo, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("target must be a Git repository")
	}
	if top != repo {
		return "", fmt.Errorf("target must be repository root")
	}
	dirty, err := git(ctx, repo, "status", "--porcelain")
	if err != nil {
		return "", err
	}
	if dirty != "" {
		return "", fmt.Errorf("target repository must be clean")
	}
	base, err := git(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	if o.BaseBranch == "" {
		o.BaseBranch, err = git(ctx, repo, "symbolic-ref", "--short", "HEAD")
		if err != nil {
			return "", fmt.Errorf("provide --base for a detached target")
		}
	}
	if o.Publish && !regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`).MatchString(o.GitHubRepo) {
		return "", fmt.Errorf("--publish requires --github-repo owner/name")
	}
	idBytes := make([]byte, 4)
	if _, err = rand.Read(idBytes); err != nil {
		return "", err
	}
	id := time.Now().UTC().Format("20060102T150405") + "-" + hex.EncodeToString(idBytes)
	dir := o.Dir
	if dir == "" {
		dir = filepath.Join("runs", id)
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if dir == repo || strings.HasPrefix(dir, repo+string(filepath.Separator)) {
		return "", fmt.Errorf("run directory must be outside target repository")
	}
	if err = os.Mkdir(dir, 0700); err != nil {
		if !os.IsNotExist(err) {
			return "", err
		}
		if err = os.MkdirAll(filepath.Dir(dir), 0755); err != nil {
			return "", err
		}
		if err = os.Mkdir(dir, 0700); err != nil {
			return "", err
		}
	}
	r := Run{ID: id, Repo: repo, Worktree: filepath.Join(dir, "worktree"), Branch: "codex/devflow/" + id, Base: base, Task: o.Task, Status: "running", Publish: o.Publish, GitHubRepo: o.GitHubRepo, BaseBranch: o.BaseBranch, Created: time.Now().UTC().Format(time.RFC3339), Runner: o.Runner, BuildPackage: o.BuildPackage}
	r.AssetHashes, err = snapshot(dir, o.Rules)
	if err != nil {
		return dir, err
	}
	if _, err = policy(dir); err != nil {
		return dir, err
	}
	if _, err = git(ctx, repo, "worktree", "add", "-b", r.Branch, r.Worktree, base); err != nil {
		return dir, err
	}
	if _, err = os.Stat(filepath.Join(r.Worktree, "go.mod")); err != nil {
		return dir, fmt.Errorf("target needs go.mod")
	}
	if err = save(dir, &r); err != nil {
		return dir, err
	}
	return dir, nil
}
func (e Engine) Resume(ctx context.Context, dir, answer string) error {
	unlock, err := lock(dir)
	if err != nil {
		return err
	}
	defer unlock()
	r, err := Load(dir)
	if err != nil {
		return err
	}
	if err = verify(dir, r); err != nil {
		return err
	}
	p, err := policy(dir)
	if err != nil {
		return err
	}
	if r.Status == "released" || r.Status == "awaiting_release" {
		e.log(r.Status)
		return nil
	}
	if r.Status == "blocked" {
		return fmt.Errorf("repair budget exhausted; inspect artifacts and start a corrected task")
	}
	if r.Status == "awaiting_input" && strings.TrimSpace(answer) == "" {
		return fmt.Errorf("provide --answer to resolve clarification questions")
	}
	if answer != "" {
		if r.Stage != 0 {
			return fmt.Errorf("answers are accepted only during clarification")
		}
		r.Answers = append(r.Answers, answer)
	}
	r.Status = "running"
	r.Error = ""
	if err = save(dir, &r); err != nil {
		return err
	}
	fail := func(cause error) error {
		r.Status = "failed"
		r.Error = cause.Error()
		if se := save(dir, &r); se != nil {
			return fmt.Errorf("%v; saving state: %w", cause, se)
		}
		return cause
	}
	for r.Stage < len(Stages) {
		if err = ctx.Err(); err != nil {
			return fail(err)
		}
		stage := Stages[r.Stage]
		e.log("→ " + stage)
		input, err := stageInput(ctx, dir, r, p)
		if err != nil {
			return fail(err)
		}
		skill, err := os.ReadFile(filepath.Join(dir, "rules", "skills", stage, "SKILL.md"))
		if err != nil {
			return fail(err)
		}
		rules, _ := os.ReadFile(filepath.Join(dir, "rules", "invariants.json"))
		req := ai.Request{Stage: stage, Instructions: string(skill) + "\nMandatory invariants:\n" + string(rules), Input: input, Schema: Schema(stage)}
		attempt := len(r.Records) + 1
		if err = writeJSON(filepath.Join(dir, fmt.Sprintf("request-%03d.json", attempt)), req); err != nil {
			return fail(err)
		}
		result, err := e.Model.Complete(ctx, req)
		if err != nil {
			return fail(err)
		}
		if err = validateShape(stage, result.Output); err != nil {
			return fail(err)
		}
		doc, err := parseDocument(result.Output)
		if err != nil {
			return fail(err)
		}
		if err = validateDocument(dir, r, stage, doc, p); err != nil {
			return fail(err)
		}
		artifact := fmt.Sprintf("%03d-%s.json", attempt, stage)
		if err = atomicWrite(filepath.Join(dir, artifact), result.Output); err != nil {
			return fail(err)
		}
		r.Records = append(r.Records, Record{Stage: stage, Artifact: artifact, Hash: hash(result.Output), AI: ai.Result{ID: result.ID, Usage: result.Usage, DurationMS: result.DurationMS}})
		if err = save(dir, &r); err != nil {
			return err
		}
		switch stage {
		case "clarify":
			if len(doc.Questions) > 0 {
				r.Status = "awaiting_input"
				if err = save(dir, &r); err != nil {
					return err
				}
				for _, q := range doc.Questions {
					e.log("? " + q)
				}
				return nil
			}
		case "implementation":
			spec, err := latest(dir, r, "specification")
			if err != nil {
				return fail(err)
			}
			if err = applyFiles(r.Worktree, doc.Files, spec.PlannedFiles, p); err != nil {
				return fail(err)
			}
			tc, err := latest(dir, r, "test_cases")
			if err != nil {
				return fail(err)
			}
			before, err := repositoryContext(ctx, r, p)
			if err != nil {
				return fail(err)
			}
			logs, checkErr := checks(ctx, r, p, tc.Cases)
			if err = atomicWrite(filepath.Join(dir, fmt.Sprintf("checks-%03d.log", attempt)), []byte(logs)); err != nil {
				return fail(err)
			}
			after, err := repositoryContext(ctx, r, p)
			if err != nil {
				return fail(err)
			}
			if before != after {
				return fail(fmt.Errorf("checks modified source files; inspect worktree before retry"))
			}
			if checkErr != nil {
				if err = repair(dir, &r, p, logs+"\n"+checkErr.Error()); err != nil {
					return err
				}
				e.log("  checks failed; requesting repair")
				continue
			}
			if err = checkChangedPaths(ctx, r, spec.PlannedFiles); err != nil {
				return fail(err)
			}
			if _, err = git(ctx, r.Worktree, "add", "--", "."); err != nil {
				return fail(err)
			}
			diff, err := git(ctx, r.Worktree, "diff", "--cached", "--name-only")
			if err != nil {
				return fail(err)
			}
			if diff != "" {
				if _, err = git(ctx, r.Worktree, "-c", "user.name=Devflow", "-c", "user.email=devflow@localhost", "commit", "-m", "Implement: "+firstLine(r.Task)); err != nil {
					return fail(err)
				}
			}
			r.CheckedSHA, err = git(ctx, r.Worktree, "rev-parse", "HEAD")
			if err != nil {
				return fail(err)
			}
			if r.CheckedSHA == r.Base {
				return fail(fmt.Errorf("implementation produced no changes"))
			}
			r.Records[len(r.Records)-1].Commit = r.CheckedSHA
			r.Feedback = logs
			if hasStage(r, "pull_request") {
				r.Stage = 5
				if err = save(dir, &r); err != nil {
					return err
				}
				continue
			}
		case "pull_request":
			if err = ensureChecked(ctx, r); err != nil {
				return fail(err)
			}
			if err = atomicWrite(filepath.Join(dir, "pull-request.md"), []byte(doc.Report+"\n")); err != nil {
				return fail(err)
			}
			if r.Publish {
				if err = publishPR(ctx, dir, &r, doc.Report); err != nil {
					return fail(err)
				}
			}
		case "review":
			if err = ensureChecked(ctx, r); err != nil {
				return fail(err)
			}
			blocking := []Finding{}
			for _, f := range doc.Findings {
				if f.Severity != "low" {
					blocking = append(blocking, f)
				}
			}
			if len(blocking) > 0 {
				b, _ := json.Marshal(blocking)
				if err = repair(dir, &r, p, string(b)); err != nil {
					return err
				}
				r.Stage = 3
				r.ReviewedSHA = ""
				r.ReviewedTree = ""
				if err = save(dir, &r); err != nil {
					return err
				}
				e.log("  review requested changes")
				continue
			}
			r.ReviewedSHA = r.CheckedSHA
			r.ReviewedTree, err = git(ctx, r.Worktree, "rev-parse", "HEAD^{tree}")
			if err != nil {
				return fail(err)
			}
			if r.Publish {
				if _, err = git(ctx, r.Worktree, "push", "origin", "HEAD:refs/heads/"+r.Branch); err != nil {
					return fail(err)
				}
				if _, err = gh(ctx, r, "pr", "ready", r.PRURL); err != nil {
					// A resumed run may have already marked this PR ready.
					state, se := gh(ctx, r, "pr", "view", r.PRURL, "--json", "isDraft", "--jq", ".isDraft")
					if se != nil || strings.TrimSpace(state) != "false" {
						return fail(err)
					}
				}
			}
		case "release":
			if err = ensureReviewed(ctx, r); err != nil {
				return fail(err)
			}
			if err = atomicWrite(filepath.Join(dir, "release-notes.md"), []byte(doc.Report+"\n")); err != nil {
				return fail(err)
			}
		}
		if r.CheckedSHA != "" {
			r.Records[len(r.Records)-1].Commit = r.CheckedSHA
		}
		r.Stage++
		r.Error = ""
		if err = save(dir, &r); err != nil {
			return err
		}
		e.log("✓ " + stage)
	}
	r.Status = "awaiting_release"
	if err = save(dir, &r); err != nil {
		return err
	}
	e.log("Release prepared. Approve SHA: " + r.ReviewedSHA)
	return nil
}
func repair(dir string, r *Run, p Policy, feedback string) error {
	r.Repairs++
	r.Feedback = feedback
	if r.Repairs > p.MaxRepairs {
		r.Status = "blocked"
		r.Error = "repair budget exhausted"
		if e := save(dir, r); e != nil {
			return e
		}
		return fmt.Errorf("repair budget exhausted")
	}
	return save(dir, r)
}
func hasStage(r Run, s string) bool {
	for _, rec := range r.Records {
		if rec.Stage == s {
			return true
		}
	}
	return false
}
func firstLine(s string) string {
	s = strings.TrimSpace(strings.SplitN(s, "\n", 2)[0])
	if len(s) > 100 {
		s = s[:100]
	}
	return s
}
func stageInput(ctx context.Context, dir string, r Run, p Policy) (string, error) {
	src, err := repositoryContext(ctx, r, p)
	if err != nil {
		return "", err
	}
	input := map[string]any{"task": r.Task, "answers": r.Answers, "repository": src, "feedback": r.Feedback, "base_sha": r.Base, "checked_sha": r.CheckedSHA, "reviewed_sha": r.ReviewedSHA, "build_package": r.BuildPackage}
	docs := map[string]Document{}
	for _, s := range []string{"clarify", "test_cases", "specification", "pull_request", "review"} {
		if hasStage(r, s) {
			d, e := latest(dir, r, s)
			if e != nil {
				return "", e
			}
			docs[s] = d
		}
	}
	input["artifacts"] = docs
	if r.Stage >= 4 {
		diff, e := git(ctx, r.Worktree, "diff", "--no-ext-diff", "--no-textconv", r.Base, "HEAD", "--")
		if e != nil {
			return "", e
		}
		input["diff"] = diff
	}
	b, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	if len(b) > p.MaxContext {
		return "", fmt.Errorf("stage context exceeds configured budget")
	}
	return string(b), nil
}
func validateDocument(dir string, r Run, stage string, d Document, p Policy) error {
	switch stage {
	case "clarify":
		if len(d.Criteria) == 0 {
			return fmt.Errorf("at least one acceptance criterion required")
		}
		seen := map[string]bool{}
		for _, c := range d.Criteria {
			if strings.TrimSpace(c.ID) == "" || strings.TrimSpace(c.Text) == "" || seen[c.ID] {
				return fmt.Errorf("invalid/duplicate criterion")
			}
			seen[c.ID] = true
		}
		for _, q := range d.Questions {
			if strings.TrimSpace(q) == "" {
				return fmt.Errorf("empty question")
			}
		}
	case "test_cases":
		req, e := latest(dir, r, "clarify")
		if e != nil {
			return e
		}
		criteria := map[string]bool{}
		covered := map[string]bool{}
		for _, c := range req.Criteria {
			criteria[c.ID] = true
		}
		seen := map[string]bool{}
		tests := map[string]bool{}
		kinds := map[string]bool{}
		for _, tc := range d.Cases {
			key := tc.TestFile + ":" + tc.TestName
			if tc.ID == "" || seen[tc.ID] || !criteria[tc.Criterion] || !allowed(tc.TestFile) || !strings.HasSuffix(tc.TestFile, "_test.go") || !regexp.MustCompile(`^Test[A-Z][A-Za-z0-9_]*$`).MatchString(tc.TestName) || tests[key] || tc.Steps == "" || tc.Expected == "" {
				return fmt.Errorf("invalid/duplicate test case %s", tc.ID)
			}
			if tc.Kind != "positive" && tc.Kind != "negative" && tc.Kind != "boundary" {
				return fmt.Errorf("invalid case kind")
			}
			seen[tc.ID] = true
			tests[key] = true
			kinds[tc.Kind] = true
			covered[tc.Criterion] = true
		}
		if len(covered) != len(criteria) || len(kinds) != 3 {
			return fmt.Errorf("cases must cover all criteria and positive/negative/boundary scenarios")
		}
	case "specification":
		if d.Design == "" || len(d.PlannedFiles) == 0 || len(d.PlannedFiles) > p.MaxFiles {
			return fmt.Errorf("design and bounded file plan required")
		}
		seen := map[string]bool{}
		for _, n := range d.PlannedFiles {
			if !allowed(n) || seen[n] {
				return fmt.Errorf("invalid planned file: %s", n)
			}
			seen[n] = true
		}
		tc, e := latest(dir, r, "test_cases")
		if e != nil {
			return e
		}
		for _, c := range tc.Cases {
			if !seen[c.TestFile] {
				return fmt.Errorf("test file missing from specification: %s", c.TestFile)
			}
		}
	case "implementation":
		if len(d.Files) == 0 {
			return fmt.Errorf("implementation files required")
		}
	case "review":
		for _, f := range d.Findings {
			if f.ID == "" || f.Detail == "" || (f.Severity != "low" && f.Severity != "medium" && f.Severity != "high") {
				return fmt.Errorf("invalid review finding")
			}
		}
	}
	return nil
}
func checkChangedPaths(ctx context.Context, r Run, planned []string) error {
	plan := map[string]bool{}
	for _, n := range planned {
		plan[n] = true
	}
	// Include all changes since base, including committed repair iterations.
	tracked, e := git(ctx, r.Worktree, "diff", "--name-only", "--no-renames", r.Base, "--")
	if e != nil {
		return e
	}
	others, e := git(ctx, r.Worktree, "ls-files", "--others", "--exclude-standard")
	if e != nil {
		return e
	}
	for _, n := range strings.Split(tracked+"\n"+others, "\n") {
		if n != "" && (!allowed(n) || !plan[n]) {
			return fmt.Errorf("undeclared change: %s", n)
		}
	}
	return nil
}
func ensureChecked(ctx context.Context, r Run) error {
	head, e := git(ctx, r.Worktree, "rev-parse", "HEAD")
	if e != nil {
		return e
	}
	dirty, e := git(ctx, r.Worktree, "status", "--porcelain")
	if e != nil {
		return e
	}
	if r.CheckedSHA == "" || head != r.CheckedSHA || dirty != "" {
		return fmt.Errorf("worktree changed after checks; a fresh implementation/check cycle is required")
	}
	return nil
}
func ensureReviewed(ctx context.Context, r Run) error {
	if e := ensureChecked(ctx, r); e != nil {
		return e
	}
	tree, e := git(ctx, r.Worktree, "rev-parse", "HEAD^{tree}")
	if e != nil {
		return e
	}
	if r.ReviewedSHA == "" || r.ReviewedSHA != r.CheckedSHA || tree != r.ReviewedTree {
		return fmt.Errorf("review does not match current commit/tree")
	}
	return nil
}
