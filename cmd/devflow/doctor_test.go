package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"devflow/internal/ai"
	"devflow/internal/config"
)

type call struct {
	name    string
	args    []string
	env     []string
	timeout time.Duration
}

type fakeModel struct {
	mu    sync.Mutex
	calls int
	fn    func(context.Context, ai.Request) (ai.Result, error)
}

func (f *fakeModel) Complete(ctx context.Context, req ai.Request) (ai.Result, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.fn != nil {
		return f.fn(ctx, req)
	}
	return ai.Result{ID: "resp_test", Usage: ai.Usage{InputTokens: 1, OutputTokens: 2}}, nil
}

func (f *fakeModel) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func TestDoctorDockerPreflightOrderAndAICall(t *testing.T) {
	var calls []call
	m := &fakeModel{}
	err := runDoctor(context.Background(), doctorOptions{
		runner: "docker",
		runCmd: func(_ context.Context, timeout time.Duration, name string, args []string, env []string) error {
			calls = append(calls, call{name: name, args: append([]string(nil), args...), env: append([]string(nil), env...), timeout: timeout})
			return nil
		},
		loadConfig: func(string) (config.AI, error) { return config.AI{Key: "k", Model: "m", Timeout: time.Second}, nil },
		newModel:   func(config.AI) ai.Model { return m },
		stdout:     &strings.Builder{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 {
		t.Fatalf("expected 3 preflight calls, got %d", len(calls))
	}
	got := []string{calls[0].name + " " + strings.Join(calls[0].args, " "), calls[1].name + " " + strings.Join(calls[1].args, " "), calls[2].name + " " + strings.Join(calls[2].args, " ")}
	want := []string{"git --version", "docker --version", "docker info --format {{.ServerVersion}}"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order mismatch at %d: got %q want %q", i, got[i], want[i])
		}
	}
	if m.Calls() != 1 {
		t.Fatalf("expected AI call once, got %d", m.Calls())
	}
}

func TestDoctorLocalPreflightSkipsDocker(t *testing.T) {
	var out strings.Builder
	var calls []call
	m := &fakeModel{}
	err := runDoctor(context.Background(), doctorOptions{runner: "local", stdout: &out,
		runCmd: func(_ context.Context, _ time.Duration, name string, args []string, _ []string) error {
			calls = append(calls, call{name: name, args: args})
			return nil
		},
		loadConfig: func(string) (config.AI, error) { return config.AI{Key: "k", Model: "m", Timeout: time.Second}, nil },
		newModel:   func(config.AI) ai.Model { return m },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].name != "git" {
		t.Fatalf("unexpected preflight calls: %#v", calls)
	}
	if !strings.Contains(out.String(), "Docker preflight: skipped") {
		t.Fatalf("missing docker skip output: %q", out.String())
	}
}

func TestDoctorInvalidRunnerNoAICall(t *testing.T) {
	m := &fakeModel{}
	err := runDoctor(context.Background(), doctorOptions{runner: "bad", newModel: func(config.AI) ai.Model { return m }})
	if err == nil || !strings.Contains(err.Error(), "invalid runner") {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Calls() != 0 {
		t.Fatal("AI must not be called")
	}
}

func TestDoctorPreflightFailuresNoAICall(t *testing.T) {
	cases := []struct {
		name   string
		runner string
		runCmd func(context.Context, time.Duration, string, []string, []string) error
	}{
		{name: "git_missing", runner: "docker", runCmd: func(_ context.Context, _ time.Duration, name string, _ []string, _ []string) error {
			if name == "git" {
				return exec.ErrNotFound
			}
			return nil
		}},
		{name: "docker_missing", runner: "docker", runCmd: func(_ context.Context, _ time.Duration, name string, args []string, _ []string) error {
			if name == "docker" && len(args) > 0 && args[0] == "--version" {
				return exec.ErrNotFound
			}
			return nil
		}},
		{name: "daemon", runner: "docker", runCmd: func(_ context.Context, _ time.Duration, name string, args []string, _ []string) error {
			if name == "docker" && len(args) > 0 && args[0] == "info" {
				return errors.New("cannot connect daemon")
			}
			return nil
		}},
		{name: "timeout", runner: "docker", runCmd: func(_ context.Context, _ time.Duration, _ string, _ []string, _ []string) error {
			return context.DeadlineExceeded
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &fakeModel{}
			err := runDoctor(context.Background(), doctorOptions{runner: tc.runner, newModel: func(config.AI) ai.Model { return m }, runCmd: tc.runCmd})
			if err == nil {
				t.Fatal("expected error")
			}
			if m.Calls() != 0 {
				t.Fatal("AI must not be called")
			}
		})
	}
}

func TestDoctorCanceledContextBeforePreflight(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := 0
	err := runDoctor(ctx, doctorOptions{runCmd: func(context.Context, time.Duration, string, []string, []string) error { called++; return nil }})
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("unexpected: %v", err)
	}
	if called != 0 {
		t.Fatal("preflight should not run")
	}
}

func TestDoctorCancellationCheckedImmediatelyBeforeAI(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	m := &fakeModel{}
	err := runDoctor(ctx, doctorOptions{runner: "local",
		runCmd: func(_ context.Context, _ time.Duration, _ string, _ []string, _ []string) error { return nil },
		loadConfig: func(string) (config.AI, error) {
			cancel()
			return config.AI{Key: "k", Model: "m", Timeout: time.Second}, nil
		},
		newModel: func(config.AI) ai.Model { return m },
		stdout:   &strings.Builder{},
	})
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("unexpected: %v", err)
	}
	if m.Calls() != 0 {
		t.Fatal("AI must not be called")
	}
}

func TestDoctorCommandInvocationAndEnvAllowlist(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "secret")
	t.Setenv("GITHUB_TOKEN", "secret")
	t.Setenv("GH_TOKEN", "secret")
	t.Setenv("PATH", os.Getenv("PATH"))
	var captured []call
	err := runDoctor(context.Background(), doctorOptions{runner: "local",
		runCmd: func(_ context.Context, timeout time.Duration, name string, args []string, env []string) error {
			captured = append(captured, call{name: name, args: append([]string{}, args...), env: append([]string{}, env...), timeout: timeout})
			return nil
		},
		loadConfig: func(string) (config.AI, error) { return config.AI{Key: "k", Model: "m", Timeout: time.Second}, nil },
		newModel:   func(config.AI) ai.Model { return &fakeModel{} },
		stdout:     &strings.Builder{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if captured[0].name != "git" || len(captured[0].args) != 1 || captured[0].args[0] != "--version" {
		t.Fatal("not direct argv invocation")
	}
	if captured[0].timeout > 5*time.Second {
		t.Fatal("timeout exceeded 5s")
	}
	envJoin := strings.Join(captured[0].env, "\n")
	if strings.Contains(envJoin, "OPENAI_API_KEY") || strings.Contains(envJoin, "GITHUB_TOKEN") || strings.Contains(envJoin, "GH_TOKEN") {
		t.Fatal("secret env leaked")
	}
}

func TestDoctorSanitizesReturnedErrors(t *testing.T) {
	err := runDoctor(context.Background(), doctorOptions{runner: "local",
		runCmd: func(context.Context, time.Duration, string, []string, []string) error {
			return errors.New("OPENAI_API_KEY=sk-test-secret GITHUB_TOKEN=ghs_secret")
		},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if strings.Contains(msg, "sk-test-secret") || strings.Contains(msg, "ghs_secret") {
		t.Fatalf("secret leaked: %q", msg)
	}
}

func TestDoctorSuccessOutputPreservedAfterPreflight(t *testing.T) {
	var out strings.Builder
	err := runDoctor(context.Background(), doctorOptions{runner: "local", stdout: &out,
		runCmd: func(context.Context, time.Duration, string, []string, []string) error { return nil },
		loadConfig: func(string) (config.AI, error) {
			return config.AI{Key: "k", Model: "m1", Timeout: 3 * time.Second}, nil
		},
		newModel: func(config.AI) ai.Model {
			return &fakeModel{fn: func(context.Context, ai.Request) (ai.Result, error) {
				return ai.Result{ID: "resp_1", Usage: ai.Usage{InputTokens: 11, OutputTokens: 22}}, nil
			}}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if !strings.Contains(s, "model=m1") || !strings.Contains(s, "timeout=3s") {
		t.Fatalf("missing config summary: %q", s)
	}
	if !strings.Contains(s, "AI API OK; response=resp_1; input_tokens=11; output_tokens=22") {
		t.Fatalf("missing api summary: %q", s)
	}
}

func TestMainDoctorPreflightBeforeConfigLoad(t *testing.T) {
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()
	os.Args = []string{"devflow", "doctor", "--runner", "docker"}

	oldRun := defaultDoctorRunCmd
	oldLoad := defaultDoctorConfigLoad
	oldModel := defaultDoctorModelFactory
	defer func() {
		defaultDoctorRunCmd = oldRun
		defaultDoctorConfigLoad = oldLoad
		defaultDoctorModelFactory = oldModel
	}()

	defaultDoctorRunCmd = func(context.Context, time.Duration, string, []string, []string) error {
		return exec.ErrNotFound
	}
	defaultDoctorConfigLoad = func(string) (config.AI, error) {
		return config.AI{}, errors.New("must not load config")
	}
	defaultDoctorModelFactory = func(config.AI) ai.Model {
		t.Fatal("must not create model")
		return nil
	}

	if err := run(); err == nil || !strings.Contains(err.Error(), "preflight") {
		t.Fatalf("expected preflight error, got: %v", err)
	}
}
