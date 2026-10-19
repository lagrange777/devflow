package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

func gh(ctx context.Context, r Run, args ...string) (string, error) {
	args = append(args, "--repo", r.GitHubRepo)
	return command(ctx, r.Worktree, 90, "gh", args...)
}
func verifyRemote(ctx context.Context, r Run) error {
	s, e := git(ctx, r.Worktree, "remote", "get-url", "origin")
	if e != nil {
		return e
	}
	s = strings.TrimSuffix(s, ".git")
	if s != "https://github.com/"+r.GitHubRepo && s != "git@github.com:"+r.GitHubRepo && s != "ssh://git@github.com/"+r.GitHubRepo {
		return fmt.Errorf("origin must match github.com/%s", r.GitHubRepo)
	}
	return nil
}
func publishPR(ctx context.Context, dir string, r *Run, report string) error {
	if e := verifyRemote(ctx, *r); e != nil {
		return e
	}
	if _, e := git(ctx, r.Worktree, "push", "-u", "origin", "HEAD:refs/heads/"+r.Branch); e != nil {
		return e
	}
	// Reconcile by branch after interruption instead of creating duplicate PRs.
	url, e := gh(ctx, *r, "pr", "view", r.Branch, "--json", "url", "--jq", ".url")
	if e == nil && strings.HasPrefix(strings.TrimSpace(url), "https://github.com/") {
		r.PRURL = strings.TrimSpace(url)
		_, e = gh(ctx, *r, "pr", "edit", r.PRURL, "--body-file", filepath.Join(dir, "pull-request.md"))
		return e
	}
	url, e = gh(ctx, *r, "pr", "create", "--draft", "--base", r.BaseBranch, "--head", r.Branch, "--title", strings.TrimLeft(firstLine(report), "# "), "--body-file", filepath.Join(dir, "pull-request.md"))
	if e != nil {
		return e
	}
	r.PRURL = strings.TrimSpace(url)
	return nil
}
func verifyMerged(ctx context.Context, r Run) error {
	if e := verifyRemote(ctx, r); e != nil {
		return e
	}
	s, e := gh(ctx, r, "pr", "view", r.PRURL, "--json", "state,headRefOid")
	if e != nil {
		return e
	}
	var pr struct{ State, HeadRefOid string }
	if e = json.Unmarshal([]byte(s), &pr); e != nil {
		return e
	}
	if pr.State != "MERGED" || pr.HeadRefOid != r.ReviewedSHA {
		return fmt.Errorf("published release requires a merged PR with the reviewed head SHA")
	}
	s, e = gh(ctx, r, "pr", "checks", r.PRURL, "--required", "--json", "bucket")
	if e != nil {
		return fmt.Errorf("required GitHub checks are not verified")
	}
	var checks []struct{ Bucket string }
	if e = json.Unmarshal([]byte(s), &checks); e != nil {
		return e
	}
	if len(checks) == 0 {
		return fmt.Errorf("configure at least one required GitHub check before publishing")
	}
	for _, c := range checks {
		if c.Bucket != "pass" {
			return fmt.Errorf("required GitHub check has not passed")
		}
	}
	return nil
}
