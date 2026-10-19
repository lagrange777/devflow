package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadEnvDataAndPrecedence(t *testing.T) {
	for _, k := range []string{"OPENAI_API_KEY", "OPENAI_MODEL", "OPENAI_BASE_URL", "OPENAI_TIMEOUT"} {
		t.Setenv(k, "")
	}
	p := filepath.Join(t.TempDir(), ".env")
	if e := os.WriteFile(p, []byte("export OPENAI_API_KEY='test-secret'\nOPENAI_MODEL=file-model\nOPENAI_BASE_URL=https://example.com/v1\nOPENAI_TIMEOUT=3m\nEVIL=$(touch /tmp/devflow-should-never-exist)\n"), 0600); e != nil {
		t.Fatal(e)
	}
	t.Setenv("OPENAI_MODEL", "env-model")
	c, e := Load(p)
	if e != nil {
		t.Fatal(e)
	}
	if c.Key != "test-secret" || c.Model != "env-model" || c.Timeout.Minutes() != 3 {
		t.Fatalf("unexpected config (secrets hidden)")
	}
	if os.Getenv("EVIL") != "" {
		t.Fatal("unexpected process mutation")
	}
}
func TestRejectInsecureEndpoint(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test")
	t.Setenv("OPENAI_BASE_URL", "http://example.com/v1")
	if _, e := Load(""); e == nil {
		t.Fatal("insecure endpoint accepted")
	}
}
