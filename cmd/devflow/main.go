package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"devflow/internal/ai"
	"devflow/internal/config"
	"devflow/internal/flow"
)

var defaultDoctorRunCmd doctorCommandRunner = doctorExecCommand
var defaultDoctorConfigLoad doctorConfigLoader = config.Load
var defaultDoctorModelFactory doctorModelFactory = func(c config.AI) ai.Model { return ai.New(c) }

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "devflow:", err)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) < 2 {
		return usage()
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	switch cmd {
	case "init":
		dir := fs.String("dir", "devflow-rules", "destination for editable skills and invariants")
		if e := fs.Parse(os.Args[2:]); e != nil {
			return e
		}
		return flow.Init(*dir)
	case "status":
		dir := fs.String("run", "", "run directory")
		if e := fs.Parse(os.Args[2:]); e != nil {
			return e
		}
		if *dir == "" {
			return fmt.Errorf("--run required")
		}
		r, e := flow.Load(*dir)
		if e != nil {
			return e
		}
		b, e := json.MarshalIndent(r, "", "  ")
		if e != nil {
			return e
		}
		fmt.Println(string(b))
		return nil
	case "start", "resume", "release", "doctor":
		env := fs.String("env-file", "", "AI settings file, e.g. ../codex-chat-cli/.env; never executed or copied")
		dir := fs.String("run", "", "run directory (new, for start)")
		repo := fs.String("repo", "", "clean target Git repository")
		task := fs.String("task", "", "development task")
		rules := fs.String("rules", "", "custom rules directory created by init")
		runner := fs.String("runner", "docker", "docker (isolated) or local (trusted code only)")
		pkg := fs.String("package", ".", "main package to build")
		publish := fs.Bool("publish", false, "push branch, open draft GitHub PR and publish approved release")
		githubRepo := fs.String("github-repo", "", "owner/name; required with --publish")
		base := fs.String("base", "", "target base branch (defaults to current branch)")
		answer := fs.String("answer", "", "clarification answer for resume")
		sha := fs.String("approve-sha", "", "exact reviewed SHA; explicit release approval")
		version := fs.String("version", "", "release tag, e.g. v0.1.0")
		if e := fs.Parse(os.Args[2:]); e != nil {
			return e
		}
		if cmd == "doctor" {
			return runDoctor(ctx, doctorOptions{runner: *runner, envFile: *env, runCmd: defaultDoctorRunCmd, loadConfig: defaultDoctorConfigLoad, newModel: defaultDoctorModelFactory, stdout: os.Stdout})
		}
		engine := flow.Engine{Log: func(s string) { fmt.Println(s) }}
		if cmd == "release" {
			if *dir == "" {
				return fmt.Errorf("--run required")
			}
			d, e := filepath.Abs(*dir)
			if e != nil {
				return e
			}
			return engine.Release(ctx, d, *sha, *version)
		}
		c, e := config.Load(*env)
		if e != nil {
			return e
		}
		engine.Model = ai.New(c)
		if cmd == "start" {
			if *repo == "" || strings.TrimSpace(*task) == "" {
				return fmt.Errorf("--repo and --task required")
			}
			d, e := flow.Start(ctx, flow.StartOptions{Repo: *repo, Task: *task, Dir: *dir, Rules: *rules, Runner: *runner, BuildPackage: *pkg, Publish: *publish, GitHubRepo: *githubRepo, BaseBranch: *base})
			if d != "" {
				fmt.Println("Run:", d)
			}
			if e != nil {
				return e
			}
			return engine.Resume(ctx, d, "")
		}
		if *dir == "" {
			return fmt.Errorf("--run required")
		}
		d, e := filepath.Abs(*dir)
		if e != nil {
			return e
		}
		return engine.Resume(ctx, d, *answer)
	default:
		return usage()
	}
}
func usage() error {
	fmt.Println(`Devflow — sequential AI development pipeline for Go

Commands:
  init     --dir devflow-rules
  doctor   --env-file ../codex-chat-cli/.env
  start    --repo /path/to/repo --task "..." --env-file ... [--runner local]
  resume   --run /path/to/run --env-file ... [--answer "..."]
  status   --run /path/to/run
  release  --run /path/to/run --approve-sha FULL_SHA --version v0.1.0

Run each command with -h for flags. Default execution uses Docker.
Local mode executes generated Go code under your user account.`)
	return nil
}
