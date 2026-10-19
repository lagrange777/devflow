package config

import (
	"bufio"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

type AI struct {
	Key, BaseURL, Model string
	Timeout             time.Duration
}

// Load reads only AI settings. The source file is data and is never executed.
// Existing process variables take precedence; keys are never copied to run state.
func Load(path string) (AI, error) {
	values := map[string]string{}
	if path != "" {
		f, e := os.Open(path)
		if e != nil {
			return AI{}, fmt.Errorf("read AI env file: %w", e)
		}
		defer f.Close()
		s := bufio.NewScanner(f)
		for s.Scan() {
			line := strings.TrimSpace(s.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			k, v, ok := strings.Cut(strings.TrimPrefix(line, "export "), "=")
			if !ok {
				continue
			}
			k = strings.TrimSpace(k)
			switch k {
			case "OPENAI_API_KEY", "OPENAI_BASE_URL", "OPENAI_MODEL", "OPENAI_TIMEOUT":
			default:
				continue
			}
			v = strings.TrimSpace(v)
			if len(v) >= 2 && (v[0] == '\'' && v[len(v)-1] == '\'' || v[0] == '"' && v[len(v)-1] == '"') {
				v = v[1 : len(v)-1]
			} else {
				v, _, _ = strings.Cut(v, " #")
				v = strings.TrimSpace(v)
			}
			values[k] = v
		}
		if e := s.Err(); e != nil {
			return AI{}, e
		}
	}
	get := func(k, d string) string {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
		if v := values[k]; v != "" {
			return v
		}
		return d
	}
	c := AI{Key: get("OPENAI_API_KEY", ""), BaseURL: strings.TrimRight(get("OPENAI_BASE_URL", "https://api.openai.com/v1"), "/"), Model: get("OPENAI_MODEL", "gpt-5.3-codex"), Timeout: 2 * time.Minute}
	if c.Key == "" {
		return c, fmt.Errorf("OPENAI_API_KEY is required; use --env-file /path/to/codex-chat-cli/.env")
	}
	u, e := url.Parse(c.BaseURL)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return c, fmt.Errorf("invalid OPENAI_BASE_URL")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1")) {
		return c, fmt.Errorf("AI API requires HTTPS (HTTP is allowed only on loopback)")
	}
	if raw := get("OPENAI_TIMEOUT", ""); raw != "" {
		c.Timeout, e = time.ParseDuration(raw)
		if e != nil || c.Timeout <= 0 {
			return c, fmt.Errorf("invalid OPENAI_TIMEOUT")
		}
	}
	return c, nil
}
