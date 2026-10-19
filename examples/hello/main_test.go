package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDefaultGreeting(t *testing.T) {
	w := httptest.NewRecorder()
	greeting(w, httptest.NewRequest("GET", "/hello", nil))
	if strings.TrimSpace(w.Body.String()) != "Hello, world!" {
		t.Fatalf("unexpected greeting: %q", w.Body.String())
	}
}
