package flow

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"devflow/assets"
)

func hash(b []byte) string { v := sha256.Sum256(b); return hex.EncodeToString(v[:]) }
func writeJSON(path string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	return atomicWrite(path, append(b, '\n'))
}
func atomicWrite(path string, b []byte) error {
	f, e := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(b)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	return os.Rename(f.Name(), path)
}
func Load(dir string) (Run, error) {
	var r Run
	b, e := os.ReadFile(filepath.Join(dir, "run.json"))
	if e != nil {
		return r, e
	}
	e = decode(b, &r)
	return r, e
}
func save(dir string, r *Run) error { return writeJSON(filepath.Join(dir, "run.json"), r) }
func lock(dir string) (func(), error) {
	f, e := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		f.Close()
		return nil, fmt.Errorf("run is already in use")
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}
func Init(dir string) error {
	if e := os.MkdirAll(dir, 0755); e != nil {
		return e
	}
	paths := []string{"invariants.json"}
	for _, s := range Stages {
		paths = append(paths, "skills/"+s+"/SKILL.md")
	}
	for _, p := range paths {
		b, e := assets.Files.ReadFile(p)
		if e != nil {
			return e
		}
		dst := filepath.Join(dir, p)
		if e = os.MkdirAll(filepath.Dir(dst), 0755); e != nil {
			return e
		}
		f, e := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if e != nil {
			return e
		}
		_, e = f.Write(b)
		ce := f.Close()
		if e != nil {
			return e
		}
		if ce != nil {
			return ce
		}
	}
	return nil
}
func snapshot(dir, custom string) (map[string]string, error) {
	hashes := map[string]string{}
	paths := []string{"invariants.json"}
	for _, s := range Stages {
		paths = append(paths, "skills/"+s+"/SKILL.md")
	}
	for _, p := range paths {
		var b []byte
		var e error
		if custom == "" {
			b, e = assets.Files.ReadFile(p)
		} else {
			b, e = os.ReadFile(filepath.Join(custom, p))
		}
		if e != nil {
			return nil, e
		}
		dst := filepath.Join(dir, "rules", p)
		if e = os.MkdirAll(filepath.Dir(dst), 0700); e != nil {
			return nil, e
		}
		if e = atomicWrite(dst, b); e != nil {
			return nil, e
		}
		hashes[p] = hash(b)
	}
	return hashes, nil
}
func verify(dir string, r Run) error {
	if r.Stage < 0 || r.Stage > len(Stages) {
		return fmt.Errorf("invalid stage")
	}
	for p, h := range r.AssetHashes {
		if !safeRelative(p) {
			return fmt.Errorf("invalid rule path")
		}
		b, e := os.ReadFile(filepath.Join(dir, "rules", p))
		if e != nil || hash(b) != h {
			return fmt.Errorf("skill/invariant snapshot changed: %s", p)
		}
	}
	if len(r.AssetHashes) != len(Stages)+1 {
		return fmt.Errorf("incomplete rule snapshot")
	}
	for _, rec := range r.Records {
		if !safeRelative(rec.Artifact) {
			return fmt.Errorf("invalid artifact path")
		}
		b, e := os.ReadFile(filepath.Join(dir, rec.Artifact))
		if e != nil || hash(b) != rec.Hash {
			return fmt.Errorf("artifact changed: %s", rec.Artifact)
		}
	}
	for i := 0; i < r.Stage; i++ {
		found := false
		for _, rec := range r.Records {
			if rec.Stage == Stages[i] {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("missing predecessor: %s", Stages[i])
		}
	}
	return nil
}
func safeRelative(p string) bool {
	return p != "" && !filepath.IsAbs(p) && filepath.Clean(p) == p && p != "." && !strings.HasPrefix(p, ".."+string(filepath.Separator)) && p != ".." && !strings.Contains(p, "\\")
}
func policy(dir string) (Policy, error) {
	var p Policy
	b, e := os.ReadFile(filepath.Join(dir, "rules", "invariants.json"))
	if e != nil {
		return p, e
	}
	if e = decode(b, &p); e != nil {
		return p, e
	}
	if p.MaxRepairs < 0 || p.MaxRepairs > 10 || p.MaxContext < 1000 || p.MaxContext > 1000000 || p.MaxFiles < 1 || p.MaxFiles > 100 || p.MaxFileBytes < 1 || p.MaxFileBytes > 1000000 || p.CommandTimeout < 1 || p.CommandTimeout > 900 || !p.RequireApproval || !p.RequireTestMapping {
		return p, fmt.Errorf("invalid policy or mandatory invariant disabled")
	}
	return p, nil
}
func latest(dir string, r Run, stage string) (Document, error) {
	for i := len(r.Records) - 1; i >= 0; i-- {
		if r.Records[i].Stage == stage {
			b, e := os.ReadFile(filepath.Join(dir, r.Records[i].Artifact))
			if e != nil {
				return Document{}, e
			}
			return parseDocument(b)
		}
	}
	return Document{}, fmt.Errorf("missing %s artifact", stage)
}
