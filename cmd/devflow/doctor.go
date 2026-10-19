package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"devflow/internal/ai"
	"devflow/internal/config"
)

type doctorCommandRunner func(ctx context.Context, timeout time.Duration, name string, args []string, env []string) error
type doctorConfigLoader func(envFile string) (config.AI, error)
type doctorModelFactory func(c config.AI) ai.Model

type doctorOptions struct {
	runner     string
	offline    bool
	envFile    string
	runCmd     doctorCommandRunner
	loadConfig doctorConfigLoader
	newModel   doctorModelFactory
	stdout     io.Writer
}

func runDoctor(ctx context.Context, opts doctorOptions) error {
	if opts.runCmd == nil {
		opts.runCmd = doctorExecCommand
	}
	if opts.loadConfig == nil {
		opts.loadConfig = config.Load
	}
	if opts.newModel == nil {
		opts.newModel = func(c config.AI) ai.Model { return ai.New(c) }
	}
	if opts.stdout == nil {
		opts.stdout = os.Stdout
	}

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("doctor preflight canceled: %w", err)
	}

	runner := strings.TrimSpace(opts.runner)
	if runner == "" {
		runner = "docker"
	}
	if runner != "docker" && runner != "local" {
		return fmt.Errorf("doctor preflight failed: invalid runner %q (allowed: docker, local)", runner)
	}

	env := doctorAllowlistedEnv()
	if err := opts.runCmd(ctx, 5*time.Second, "git", []string{"--version"}, env); err != nil {
		return sanitizeDoctorError("git", []string{"--version"}, err)
	}
	if runner == "docker" {
		if err := opts.runCmd(ctx, 5*time.Second, "docker", []string{"--version"}, env); err != nil {
			return sanitizeDoctorError("docker", []string{"--version"}, err)
		}
		if err := opts.runCmd(ctx, 5*time.Second, "docker", []string{"info", "--format", "{{.ServerVersion}}"}, env); err != nil {
			return sanitizeDoctorError("docker", []string{"info", "--format", "{{.ServerVersion}}"}, err)
		}
	} else {
		fmt.Fprintln(opts.stdout, "Docker preflight: skipped for --runner local")
	}

	if opts.offline {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("doctor preflight canceled: %w", err)
		}
		fmt.Fprintln(opts.stdout, "Preflight OK; AI probe skipped (--offline)")
		return nil
	}

	c, err := opts.loadConfig(opts.envFile)
	if err != nil {
		return err
	}
	fmt.Fprintf(opts.stdout, "AI configuration loaded; model=%s; timeout=%s; key is present (hidden)\n", c.Model, c.Timeout)

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("doctor canceled before AI probe: %w", err)
	}

	model := opts.newModel(c)
	result, err := model.Complete(ctx, ai.Request{Stage: "doctor", Instructions: "Return a JSON report with the word OK. No other work.", Input: "Check API connectivity.", Schema: map[string]any{"type": "object", "properties": map[string]any{"report": map[string]any{"type": "string"}}, "required": []string{"report"}, "additionalProperties": false}})
	if err != nil {
		return err
	}
	fmt.Fprintf(opts.stdout, "AI API OK; response=%s; input_tokens=%d; output_tokens=%d\n", result.ID, result.Usage.InputTokens, result.Usage.OutputTokens)
	return nil
}

func sanitizeDoctorError(name string, args []string, err error) error {
	cmd := name
	if len(args) > 0 {
		cmd += " " + strings.Join(args, " ")
	}
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("doctor preflight failed: %s: %w", cmd, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("doctor preflight failed: %s: %w", cmd, context.DeadlineExceeded)
	}
	if errors.Is(err, exec.ErrNotFound) {
		return fmt.Errorf("doctor preflight failed: %s: executable not found", cmd)
	}
	if name == "docker" && len(args) > 0 && args[0] == "info" {
		return fmt.Errorf("doctor preflight failed: docker info --format {{.ServerVersion}}: docker daemon unavailable")
	}
	return fmt.Errorf("doctor preflight failed: %s: command failed", cmd)
}

func doctorAllowlistedEnv() []string {
	keys := []string{"PATH", "HOME", "TMPDIR", "TEMP", "TMP", "SYSTEMROOT", "WINDIR"}
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if v, ok := os.LookupEnv(k); ok && v != "" {
			out = append(out, k+"="+v)
		}
	}
	return out
}

func doctorExecCommand(ctx context.Context, timeout time.Duration, name string, args []string, env []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, name, args...)
	cmd.Env = append([]string(nil), env...)
	if err := cmd.Run(); err != nil {
		if errors.Is(cmdCtx.Err(), context.DeadlineExceeded) {
			return cmdCtx.Err()
		}
		if errors.Is(cmdCtx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			return context.Canceled
		}
		if errors.Is(err, exec.ErrNotFound) {
			return exec.ErrNotFound
		}
		return err
	}
	return nil
}
