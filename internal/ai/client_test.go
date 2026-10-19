package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"devflow/internal/config"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func testClient(body string, status int, check func(*http.Request)) *Client {
	c := New(config.AI{Key: "test", Model: "existing-model", BaseURL: "https://example.com/v1", Timeout: time.Second})
	c.HTTP.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		if check != nil {
			check(r)
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: r}, nil
	})
	return c
}
func TestResponsesContract(t *testing.T) {
	c := testClient(`{"id":"resp_test","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"{\"report\":\"OK\"}"}]}],"usage":{"input_tokens":12,"output_tokens":4}}`, 200, func(r *http.Request) {
		if r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer test" {
			t.Error("wrong API request")
		}
		var body map[string]any
		if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
			t.Error(e)
		}
		if body["store"] != false || body["model"] != "existing-model" {
			t.Error("wrong model/store contract")
		}
		text := body["text"].(map[string]any)
		if text["format"].(map[string]any)["type"] != "json_schema" {
			t.Error("missing structured output")
		}
	})
	res, e := c.Complete(context.Background(), Request{Stage: "test", Schema: map[string]any{}})
	if e != nil {
		t.Fatal(e)
	}
	if res.ID != "resp_test" || res.Usage.InputTokens != 12 {
		t.Fatal("response lost")
	}
}
func TestIncompleteAndErrorsNeverAdvance(t *testing.T) {
	for _, body := range []string{`{"id":"r","status":"incomplete","output":[]}`, `{"id":"r","status":"completed","output":[{"type":"message","content":[{"type":"refusal","refusal":"no"}]}]}`} {
		c := testClient(body, 200, nil)
		if _, e := c.Complete(context.Background(), Request{}); e == nil {
			t.Fatal("bad result accepted")
		}
	}
}
func TestErrorBodyIsNotLeaked(t *testing.T) {
	c := testClient("secret-api-key", 401, nil)
	_, e := c.Complete(context.Background(), Request{})
	if e == nil || strings.Contains(e.Error(), "secret-api-key") {
		t.Fatal("upstream secret leaked")
	}
}
