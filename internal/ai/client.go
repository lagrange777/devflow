// Package ai uses the same Responses API transport and environment contract as
// codex-chat-cli/internal/openai, without importing that project's internal packages.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"devflow/internal/config"
)

type Request struct {
	Stage, Instructions, Input string
	Schema                     map[string]any
}
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}
type Result struct {
	ID         string          `json:"id"`
	Output     json.RawMessage `json:"output"`
	Usage      Usage           `json:"usage"`
	DurationMS int64           `json:"duration_ms"`
}
type Model interface {
	Complete(context.Context, Request) (Result, error)
}
type Client struct {
	Config config.AI
	HTTP   *http.Client
}

func New(c config.AI) *Client {
	return &Client{Config: c, HTTP: &http.Client{Timeout: c.Timeout, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (c *Client) Complete(ctx context.Context, r Request) (Result, error) {
	payload := map[string]any{"model": c.Config.Model, "instructions": r.Instructions, "input": r.Input, "store": false, "max_output_tokens": 12000, "text": map[string]any{"format": map[string]any{"type": "json_schema", "name": "devflow_" + r.Stage, "strict": true, "schema": r.Schema}}}
	b, e := json.Marshal(payload)
	if e != nil {
		return Result{}, e
	}
	req, e := http.NewRequestWithContext(ctx, "POST", c.Config.BaseURL+"/responses", bytes.NewReader(b))
	if e != nil {
		return Result{}, e
	}
	req.Header.Set("Authorization", "Bearer "+c.Config.Key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "devflow/0.1")
	start := time.Now()
	resp, e := c.HTTP.Do(req)
	if e != nil {
		return Result{}, fmt.Errorf("AI request failed (%T); check endpoint and connectivity", e)
	}
	defer resp.Body.Close()
	b, e = io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
	if e != nil {
		return Result{}, fmt.Errorf("read AI response: %w", e)
	}
	if len(b) > 8<<20 {
		return Result{}, fmt.Errorf("AI response exceeds 8 MiB")
	}
	// Do not echo upstream bodies: proxies may include secrets or request headers.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Result{}, fmt.Errorf("AI API returned HTTP %d", resp.StatusCode)
	}
	var body struct {
		ID, Status string
		Error      json.RawMessage
		Usage      Usage
		Output     []struct {
			Type    string
			Content []struct{ Type, Text, Refusal string }
		}
	}
	if e = json.Unmarshal(b, &body); e != nil {
		return Result{}, fmt.Errorf("invalid AI API response")
	}
	if body.Status != "completed" || body.ID == "" || len(body.Error) > 0 && string(body.Error) != "null" {
		return Result{}, fmt.Errorf("AI response not completed (status %q)", body.Status)
	}
	var parts []string
	for _, o := range body.Output {
		if o.Type != "message" {
			continue
		}
		for _, v := range o.Content {
			if v.Type == "refusal" {
				return Result{}, fmt.Errorf("AI declined this stage")
			}
			if v.Type == "output_text" {
				parts = append(parts, v.Text)
			}
		}
	}
	output := strings.Join(parts, "")
	if !json.Valid([]byte(output)) {
		return Result{}, fmt.Errorf("AI did not return a JSON document")
	}
	return Result{ID: body.ID, Output: json.RawMessage(output), Usage: body.Usage, DurationMS: time.Since(start).Milliseconds()}, nil
}
