package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Dvorinka/cix/internal"
)

const usage = `cix — CI companion for coding agents

usage:
  cix wait      [--run <id>] [--ref <name>] [--job <name>] [--timeout 45m]
                [--rerun-flaky] [--notify] [--artifact <path>]... [--json]
  cix push      [--tag <v> | HEAD] [--cancel-superseded] [wait flags]
  cix status    [--ref <name>] [--json]
  cix preflight [--base <ref>] [--files a.go,b.ts] [--all] [--json]
  cix history   [--job <name>] [--failures] [--json]

exit codes: 0 ok · 1 non-gating warnings · 2 gating failure/blocked ·
            3 verify/artifact gate failed · 4 timeout · 5 operational error
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(5)
	}
	args := os.Args[2:]
	var code int
	switch os.Args[1] {
	case "wait":
		code = cmdWait(args)
	case "push":
		code = cmdPush(args)
	case "status":
		code = cmdStatus(args)
	case "preflight":
		code = cmdPreflight(args)
	case "history":
		code = cmdHistory(args)
	case "help", "--help", "-h":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n%s", os.Args[1], usage)
		code = 5
	}
	os.Exit(code)
}

func waitFlags(fs *flag.FlagSet, w *internal.WaitOpts) {
	fs.Int64Var(&w.RunID, "run", 0, "run id (default: newest for ref/HEAD)")
	fs.StringVar(&w.Ref, "ref", "", "branch or tag (default: HEAD)")
	fs.StringVar(&w.Job, "job", "", "wait on a single job")
	fs.StringVar(&w.Gate, "gate", "", "deploy-path gate job override")
	fs.DurationVar(&w.Timeout, "timeout", 45*time.Minute, "max wait")
	fs.BoolVar(&w.RerunFlaky, "rerun-flaky", false, "rerun once on transient failure")
	fs.BoolVar(&w.Notify, "notify", false, "desktop notification on finish")
	fs.BoolVar(&w.JSON, "json", false, "structured output")
	fs.Var((*strList)(&w.Artifacts), "artifact", "artifact file to verify (repeatable)")
}

type strList []string

func (s *strList) String() string     { return strings.Join(*s, ",") }
func (s *strList) Set(v string) error { *s = append(*s, v); return nil }

func cmdWait(args []string) int {
	fs := flag.NewFlagSet("wait", flag.ContinueOnError)
	var w internal.WaitOpts
	waitFlags(fs, &w)
	if err := fs.Parse(args); err != nil {
		return 5
	}
	api, cfg, err := setup()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 5
	}
	return internal.Wait(api, cfg, w, os.Stdout)
}

func cmdStatus(args []string) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	var w internal.WaitOpts
	fs.StringVar(&w.Ref, "ref", "", "branch or tag")
	fs.Int64Var(&w.RunID, "run", 0, "run id")
	fs.StringVar(&w.Gate, "gate", "", "gate job override")
	fs.BoolVar(&w.JSON, "json", false, "structured output")
	if err := fs.Parse(args); err != nil {
		return 5
	}
	api, cfg, err := setup()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 5
	}
	return internal.Status(api, cfg, w, os.Stdout)
}

func cmdPush(args []string) int {
	fs := flag.NewFlagSet("push", flag.ContinueOnError)
	var p internal.PushOpts
	fs.StringVar(&p.Tag, "tag", "", "push a tag instead of HEAD")
	fs.BoolVar(&p.CancelSuperseded, "cancel-superseded", false,
		"cancel older in-progress runs for this sha")
	waitFlags(fs, &p.Wait)
	if err := fs.Parse(args); err != nil {
		return 5
	}
	api, cfg, err := setup()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 5
	}
	return internal.Push(api, cfg, p, os.Stdout)
}

func cmdPreflight(args []string) int {
	fs := flag.NewFlagSet("preflight", flag.ContinueOnError)
	var p internal.PreflightOpts
	var files string
	fs.StringVar(&p.Base, "base", "", "diff base (default: origin/HEAD)")
	fs.StringVar(&files, "files", "", "comma-separated changed files (overrides git diff)")
	fs.BoolVar(&p.All, "all", false, "run all checks even after a failure")
	fs.BoolVar(&p.JSON, "json", false, "structured output")
	if err := fs.Parse(args); err != nil {
		return 5
	}
	if files != "" {
		p.Files = strings.Split(files, ",")
	}
	cfg, err := internal.LoadConfig(".")
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 5
	}
	return internal.Preflight(cfg, p, os.Stdout)
}

func cmdHistory(args []string) int {
	fs := flag.NewFlagSet("history", flag.ContinueOnError)
	var h internal.HistoryOpts
	fs.StringVar(&h.Job, "job", "", "filter to one job")
	fs.BoolVar(&h.Failures, "failures", false, "list failures only")
	fs.BoolVar(&h.JSON, "json", false, "structured output")
	if err := fs.Parse(args); err != nil {
		return 5
	}
	return internal.History(h, os.Stdout)
}

// setup wires the gh API and .cix.yml config.
func setup() (*internal.GH, internal.Config, error) {
	cfg, err := internal.LoadConfig(".")
	if err != nil {
		return nil, cfg, err
	}
	api, err := internal.NewGH()
	if err != nil {
		return nil, cfg, err
	}
	return api, cfg, nil
}
