package flow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"devflow/internal/ai"
)

var Stages = []string{"clarify", "test_cases", "specification", "implementation", "pull_request", "review", "release"}

type Criterion struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}
type Case struct {
	ID        string `json:"id"`
	Criterion string `json:"criterion"`
	Kind      string `json:"kind"`
	Steps     string `json:"steps"`
	Expected  string `json:"expected"`
	TestFile  string `json:"test_file"`
	TestName  string `json:"test_name"`
}
type File struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}
type Finding struct {
	ID       string `json:"id"`
	Severity string `json:"severity"`
	Path     string `json:"path"`
	Detail   string `json:"detail"`
}
type Document struct {
	Report       string      `json:"report"`
	Criteria     []Criterion `json:"criteria,omitempty"`
	Questions    []string    `json:"questions,omitempty"`
	Cases        []Case      `json:"cases,omitempty"`
	Design       string      `json:"design,omitempty"`
	PlannedFiles []string    `json:"planned_files,omitempty"`
	Files        []File      `json:"files,omitempty"`
	Findings     []Finding   `json:"findings,omitempty"`
}
type Policy struct {
	MaxRepairs         int      `json:"max_repairs"`
	MaxContext         int      `json:"max_context_bytes"`
	MaxFiles           int      `json:"max_changed_files"`
	MaxFileBytes       int      `json:"max_file_bytes"`
	CommandTimeout     int      `json:"command_timeout_seconds"`
	RequireTestMapping bool     `json:"require_test_mapping"`
	RequireApproval    bool     `json:"require_release_approval"`
	Rules              []string `json:"rules"`
}
type Record struct {
	Stage    string    `json:"stage"`
	Artifact string    `json:"artifact"`
	Hash     string    `json:"sha256"`
	Commit   string    `json:"commit,omitempty"`
	AI       ai.Result `json:"ai"`
}
type Run struct {
	ID             string            `json:"id"`
	Repo           string            `json:"repo"`
	Worktree       string            `json:"worktree"`
	Branch         string            `json:"branch"`
	Base           string            `json:"base_sha"`
	Task           string            `json:"task"`
	Answers        []string          `json:"answers,omitempty"`
	Stage          int               `json:"stage"`
	Status         string            `json:"status"`
	Error          string            `json:"error,omitempty"`
	Repairs        int               `json:"repairs"`
	Feedback       string            `json:"feedback,omitempty"`
	Records        []Record          `json:"records"`
	AssetHashes    map[string]string `json:"asset_hashes"`
	Publish        bool              `json:"publish"`
	GitHubRepo     string            `json:"github_repo,omitempty"`
	BaseBranch     string            `json:"base_branch"`
	PRURL          string            `json:"pr_url,omitempty"`
	ReviewedSHA    string            `json:"reviewed_sha,omitempty"`
	ReviewedTree   string            `json:"reviewed_tree,omitempty"`
	CheckedSHA     string            `json:"checked_sha,omitempty"`
	ReleaseVersion string            `json:"release_version,omitempty"`
	ReleaseSHA     string            `json:"release_sha,omitempty"`
	ReleaseURL     string            `json:"release_url,omitempty"`
	Created        string            `json:"created"`
	Runner         string            `json:"runner"`
	BuildPackage   string            `json:"build_package"`
}

func decode(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return e
	}
	if e := d.Decode(new(any)); e != io.EOF {
		return fmt.Errorf("trailing JSON data")
	}
	return nil
}
func stringSchema() map[string]any             { return map[string]any{"type": "string"} }
func array(item map[string]any) map[string]any { return map[string]any{"type": "array", "items": item} }
func object(props map[string]any) map[string]any {
	keys := []string{}
	for k := range props {
		keys = append(keys, k)
	}
	return map[string]any{"type": "object", "properties": props, "required": keys, "additionalProperties": false}
}
func fields(names ...string) map[string]any {
	m := map[string]any{}
	for _, n := range names {
		m[n] = stringSchema()
	}
	return m
}
func Schema(stage string) map[string]any {
	p := fields("report")
	switch stage {
	case "clarify":
		p["criteria"] = array(object(fields("id", "text")))
		p["questions"] = array(stringSchema())
	case "test_cases":
		p["cases"] = array(object(fields("id", "criterion", "kind", "steps", "expected", "test_file", "test_name")))
	case "specification":
		p["design"] = stringSchema()
		p["planned_files"] = array(stringSchema())
	case "implementation":
		p["files"] = array(object(fields("path", "content")))
	case "review":
		p["findings"] = array(object(fields("id", "severity", "path", "detail")))
	}
	return object(p)
}
func parseDocument(b []byte) (Document, error) {
	var d Document
	e := decode(b, &d)
	if e != nil {
		return d, fmt.Errorf("stage JSON: %w", e)
	}
	if strings.TrimSpace(d.Report) == "" {
		return d, fmt.Errorf("stage report is required")
	}
	return d, nil
}
