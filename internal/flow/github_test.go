package flow

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPublishedReleaseRequiresMergedExactHeadAndPassingChecks(t *testing.T) {
	ctx, dir, _ := fixture(t)
	r, e := Load(dir)
	if e != nil {
		t.Fatal(e)
	}
	r.GitHubRepo = "owner/repo"
	r.PRURL = "https://github.com/owner/repo/pull/1"
	r.ReviewedSHA = "abc123"
	if _, e = git(ctx, r.Worktree, "remote", "add", "origin", "https://github.com/owner/repo.git"); e != nil {
		t.Fatal(e)
	}
	bin := t.TempDir()
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, tc := range []struct {
		name, state, sha, buckets string
		wantError                 bool
	}{
		{"unmerged", "OPEN", "abc123", `[{"bucket":"pass"}]`, true},
		{"new_head", "MERGED", "other", `[{"bucket":"pass"}]`, true},
		{"failed_check", "MERGED", "abc123", `[{"bucket":"fail"}]`, true},
		{"no_required_checks", "MERGED", "abc123", `[]`, true},
		{"valid", "MERGED", "abc123", `[{"bucket":"pass"}]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			script := "#!/bin/sh\ncase \"$2\" in\nview) printf '%s\\n' '{\"state\":\"" + tc.state + "\",\"headRefOid\":\"" + tc.sha + "\"}' ;;\nchecks) printf '%s\\n' '" + tc.buckets + "' ;;\n*) exit 1 ;;\nesac\n"
			if e = os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0755); e != nil {
				t.Fatal(e)
			}
			e = verifyMerged(ctx, r)
			if (e != nil) != tc.wantError {
				t.Fatalf("unexpected verification: %v", e)
			}
		})
	}
}
func TestCommandsDoNotInheritAISecrets(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "must-not-reach-child")
	t.Setenv("GITHUB_TOKEN", "also-private")
	if out, e := command(t.Context(), "", 5, "sh", "-c", `test -z "$OPENAI_API_KEY" && test -z "$GITHUB_TOKEN"`); e != nil {
		t.Fatalf("child received credentials: %v: %s", e, out)
	}
}
