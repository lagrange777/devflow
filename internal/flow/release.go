package flow

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

func (e Engine) Release(ctx context.Context, dir, sha, version string) error {
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
	if !regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[A-Za-z0-9.-]+)?$`).MatchString(version) {
		return fmt.Errorf("version must be a tag such as v0.1.0")
	}
	if r.Status == "released" {
		if r.ReleaseSHA == sha && r.ReleaseVersion == version {
			e.log("Already released: " + version)
			return nil
		}
		return fmt.Errorf("run already released with different SHA/version")
	}
	if r.Status != "awaiting_release" || r.Stage != len(Stages) {
		return fmt.Errorf("all stages must complete before release")
	}
	if sha == "" || sha != r.ReviewedSHA {
		return fmt.Errorf("--approve-sha must exactly match reviewed SHA: %s", r.ReviewedSHA)
	}
	if r.ReleaseVersion != "" && r.ReleaseVersion != version {
		return fmt.Errorf("release already started with version %s", r.ReleaseVersion)
	}
	if err = ensureReviewed(ctx, r); err != nil {
		return err
	}
	p, err := policy(dir)
	if err != nil {
		return err
	}
	if r.Publish {
		if err = verifyMerged(ctx, r); err != nil {
			return err
		}
	}
	// Release notes are derived from the verified artifact, never from an editable export.
	notes, err := latest(dir, r, "release")
	if err != nil {
		return err
	}
	if err = atomicWrite(filepath.Join(dir, "release-notes.md"), []byte(notes.Report+"\n")); err != nil {
		return err
	}
	// Persist explicit approval before a build or external mutation.
	r.ReleaseVersion = version
	r.ReleaseSHA = sha
	if err = save(dir, &r); err != nil {
		return err
	}
	tc, err := latest(dir, r, "test_cases")
	if err != nil {
		return err
	}
	logs, err := checks(ctx, r, p, tc.Cases)
	if we := atomicWrite(filepath.Join(dir, "release-checks.log"), []byte(logs)); we != nil {
		return we
	}
	if err != nil {
		return err
	}
	if err = ensureReviewed(ctx, r); err != nil {
		return err
	}
	outDir := filepath.Join(dir, "dist")
	if err = os.MkdirAll(outDir, 0700); err != nil {
		return err
	}
	defer os.Remove(filepath.Join(r.Worktree, ".devflow-build"))
	output, err := runGo(ctx, r, p, "build", "-trimpath", "-o", ".devflow-build", r.BuildPackage)
	if err != nil {
		return fmt.Errorf("release build: %w: %s", err, output)
	}
	binary, err := os.ReadFile(filepath.Join(r.Worktree, ".devflow-build"))
	if err != nil {
		return err
	}
	if len(binary) < 4 || string(binary[:4]) == "!<ar" {
		return fmt.Errorf("--package must build an executable main package")
	}
	if err = os.Remove(filepath.Join(r.Worktree, ".devflow-build")); err != nil {
		return err
	}
	if err = ensureReviewed(ctx, r); err != nil {
		return err
	}
	targetOS, targetArch := runtime.GOOS, runtime.GOARCH
	if r.Runner == "docker" {
		targetOS = "linux"
	}
	name := fmt.Sprintf("app-%s-%s-%s.tar.gz", version, targetOS, targetArch)
	manifest := map[string]any{"version": version, "commit": sha, "tree": r.ReviewedTree, "goos": targetOS, "goarch": targetArch, "run": r.ID, "binary_sha256": hash(binary)}
	mb, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err = atomicWrite(filepath.Join(outDir, "manifest.json"), mb); err != nil {
		return err
	}
	if err = archive(filepath.Join(outDir, name), binary, mb); err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(outDir, name))
	if err != nil {
		return err
	}
	checksums := hash(data) + "  " + name + "\n" + hash(mb) + "  manifest.json\n"
	if err = atomicWrite(filepath.Join(outDir, "SHA256SUMS"), []byte(checksums)); err != nil {
		return err
	}
	existing, tagErr := git(ctx, r.Worktree, "rev-parse", "--verify", "refs/tags/"+version)
	if tagErr == nil && existing != sha {
		return fmt.Errorf("tag %s already points elsewhere", version)
	}
	if tagErr != nil {
		if _, err = git(ctx, r.Worktree, "tag", version, sha); err != nil {
			return err
		}
	}
	r.ReleaseURL = filepath.Join(outDir, name)
	if r.Publish {
		// Only lightweight tags created above are accepted, making reconciliation exact.
		remote, e := git(ctx, r.Worktree, "ls-remote", "--tags", "origin", "refs/tags/"+version)
		if e != nil {
			return e
		}
		if remote != "" && !strings.HasPrefix(remote, sha+"\t") {
			return fmt.Errorf("remote tag differs from approved SHA")
		}
		if _, err = git(ctx, r.Worktree, "push", "origin", "refs/tags/"+version); err != nil {
			return err
		}
		url, viewErr := gh(ctx, r, "release", "view", version, "--json", "url", "--jq", ".url")
		if viewErr != nil {
			url, err = gh(ctx, r, "release", "create", version, filepath.Join(outDir, name), filepath.Join(outDir, "manifest.json"), filepath.Join(outDir, "SHA256SUMS"), "--verify-tag", "--title", version, "--notes-file", filepath.Join(dir, "release-notes.md"))
			if err != nil {
				return err
			}
		} else {
			// A previous attempt may have created the release but not persisted state.
			if _, err = gh(ctx, r, "release", "upload", version, filepath.Join(outDir, name), filepath.Join(outDir, "manifest.json"), filepath.Join(outDir, "SHA256SUMS"), "--clobber"); err != nil {
				return err
			}
		}
		r.ReleaseURL = strings.TrimSpace(url)
	}
	r.Status = "released"
	r.Error = ""
	if err = save(dir, &r); err != nil {
		return err
	}
	e.log("Released " + version + ": " + r.ReleaseURL)
	return nil
}
func archive(path string, binary, manifest []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".release-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, item := range []struct {
		name string
		data []byte
		mode int64
	}{{"app", binary, 0755}, {"manifest.json", manifest, 0644}} {
		if err = tw.WriteHeader(&tar.Header{Name: item.name, Mode: item.mode, Size: int64(len(item.data))}); err != nil {
			break
		}
		if _, err = tw.Write(item.data); err != nil {
			break
		}
	}
	te := tw.Close()
	ge := gz.Close()
	fe := f.Close()
	for _, e := range []error{err, te, ge, fe} {
		if e != nil {
			return e
		}
	}
	return os.Rename(f.Name(), path)
}
