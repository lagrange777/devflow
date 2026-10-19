package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"devflow/internal/ai"
	"devflow/internal/config"
)

type fakeModel struct{}

func (f *fakeModel) Complete(context.Context, ai.Request) (ai.Result, error) {
	return ai.Result{ID: "resp_test", Usage: ai.Usage{InputTokens: 1, OutputTokens: 2}}, nil
}

func TestDoctorOfflineDockerSuccessSkipsAI(t *testing.T) {
	var out strings.Builder
	var calls []string
	loadCalls := 0
	err := runDoctor(context.Background(), doctorOptions{
		runner:  "docker",
		offline: true,
		stdout:  &out,
		runCmd: func(_ context.Context, _ time.Duration, name string, args []string, _ []string) error {
			calls = append(calls, name+" "+strings.Join(args, " "))
			return nil
		},
		loadConfig: func(string) (config.AI, error) {
			loadCalls++
			return config.AI{}, nil
		},
		newModel: func(config.AI) ai.Model {
			t.Fatal("newModel must not be called in offline mode")
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"git --version", "docker --version", "docker info --format {{.ServerVersion}}"}
	if len(calls) != len(want) {
		t.Fatalf("unexpected preflight calls: %#v", calls)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("order mismatch at %d: got %q want %q", i, calls[i], want[i])
		}
	}
	if loadCalls != 0 {
		t.Fatalf("loadConfig called %d times", loadCalls)
	}
	if !strings.Contains(out.String(), "Preflight OK; AI probe skipped (--offline)") {
		t.Fatalf("missing offline success output: %q", out.String())
	}
}

func TestDoctorOfflineLocalSuccessSkipsDockerAndAI(t *testing.T) {
	var out strings.Builder
	var calls []string
	err := runDoctor(context.Background(), doctorOptions{
		runner:  "local",
		offline: true,
		stdout:  &out,
		runCmd: func(_ context.Context, _ time.Duration, name string, args []string, _ []string) error {
			calls = append(calls, name+" "+strings.Join(args, " "))
			return nil
		},
		loadConfig: func(string) (config.AI, error) {
			t.Fatal("loadConfig must not be called in offline mode")
			return config.AI{}, nil
		},
		newModel: func(config.AI) ai.Model {
			t.Fatal("newModel must not be called in offline mode")
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0] != "git --version" {
		t.Fatalf("unexpected preflight calls: %#v", calls)
	}
	s := out.String()
	if !strings.Contains(s, "Docker preflight: skipped for --runner local") {
		t.Fatalf("missing docker skip output: %q", s)
	}
	if !strings.Contains(s, "Preflight OK; AI probe skipped (--offline)") {
		t.Fatalf("missing offline success output: %q", s)
	}
}

func TestDoctorOfflinePreflightFailureStopsBeforeConfigModel(t *testing.T) {
	var out strings.Builder
	err := runDoctor(context.Background(), doctorOptions{
		runner:  "docker",
		offline: true,
		stdout:  &out,
		runCmd: func(_ context.Context, _ time.Duration, name string, args []string, _ []string) error {
			if name == "docker" && len(args) > 0 && args[0] == "info" {
				return errors.New("daemon down")
			}
			return nil
		},
		loadConfig: func(string) (config.AI, error) {
			t.Fatal("loadConfig must not be called when preflight fails")
			return config.AI{}, nil
		},
		newModel: func(config.AI) ai.Model {
			t.Fatal("newModel must not be called when preflight fails")
			return nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "doctor preflight failed") {
		t.Fatalf("expected preflight error, got: %v", err)
	}
	if strings.Contains(out.String(), "Preflight OK; AI probe skipped (--offline)") {
		t.Fatalf("unexpected offline success output: %q", out.String())
	}
}

func TestDoctorOfflineCanceledBeforePreflight(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	err := runDoctor(ctx, doctorOptions{
		offline: true,
		runCmd: func(context.Context, time.Duration, string, []string, []string) error {
			calls++
			return nil
		},
		loadConfig: func(string) (config.AI, error) {
			t.Fatal("loadConfig must not be called")
			return config.AI{}, nil
		},
		newModel: func(config.AI) ai.Model {
			t.Fatal("newModel must not be called")
			return nil
		},
	})
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 0 {
		t.Fatalf("preflight should not run, got %d calls", calls)
	}
}

func TestDoctorOfflineCancellationDuringPreflightStopsBeforeAI(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	first := true
	err := runDoctor(ctx, doctorOptions{
		runner:  "docker",
		offline: true,
		runCmd: func(_ context.Context, _ time.Duration, _ string, _ []string, _ []string) error {
			if first {
				first = false
				cancel()
				return context.Canceled
			}
			return nil
		},
		loadConfig: func(string) (config.AI, error) {
			t.Fatal("loadConfig must not be called")
			return config.AI{}, nil
		},
		newModel: func(config.AI) ai.Model {
			t.Fatal("newModel must not be called")
			return nil
		},
	})
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDoctorOnlineModeUnchanged(t *testing.T) {
	var out strings.Builder
	calls := 0
	m := &fakeModel{}
	err := runDoctor(context.Background(), doctorOptions{
		runner:  "local",
		offline: false,
		stdout:  &out,
		runCmd: func(_ context.Context, _ time.Duration, _ string, _ []string, _ []string) error {
			return nil
		},
		loadConfig: func(string) (config.AI, error) {
			calls++
			return config.AI{Key: "k", Model: "m", Timeout: time.Second}, nil
		},
		newModel: func(config.AI) ai.Model { return m },
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("expected loadConfig once, got %d", calls)
	}
	s := out.String()
	if !strings.Contains(s, "AI configuration loaded") || !strings.Contains(s, "AI API OK") {
		t.Fatalf("missing online output: %q", s)
	}
}

func TestMainOfflineFlagOnlyForDoctor(t *testing.T) {
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()

	oldRun := defaultDoctorRunCmd
	oldLoad := defaultDoctorConfigLoad
	oldModel := defaultDoctorModelFactory
	defer func() {
		defaultDoctorRunCmd = oldRun
		defaultDoctorConfigLoad = oldLoad
		defaultDoctorModelFactory = oldModel
	}()

	defaultDoctorRunCmd = func(context.Context, time.Duration, string, []string, []string) error { return nil }
	defaultDoctorConfigLoad = func(string) (config.AI, error) {
		return config.AI{}, errors.New("must not load config in offline mode")
	}
	defaultDoctorModelFactory = func(config.AI) ai.Model {
		return &fakeModel{}
	}

	os.Args = []string{"devflow", "doctor", "--offline", "--runner", "local"}
	if err := run(); err != nil {
		t.Fatalf("doctor --offline should parse and run: %v", err)
	}

	os.Args = []string{"devflow", "status", "--offline"}
	if err := run(); err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
		t.Fatalf("expected unknown flag error for non-doctor command, got: %v", err)
	}
}
