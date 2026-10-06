package internal

import (
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path"
	"strings"
	"time"
)

// CheckResult is one preflight check outcome.
type CheckResult struct {
	Name     string `json:"name"`
	Matched  bool   `json:"matched"`
	Command  string `json:"command"`
	Passed   bool   `json:"passed"`
	Duration int    `json:"duration_seconds"`
	Output   string `json:"output"`
	Optional bool   `json:"optional,omitempty"`
}

// PreflightResult is the preflight JSON contract.
type PreflightResult struct {
	Checks         []CheckResult `json:"checks"`
	Blocked        bool          `json:"blocked"`
	FailedRequired int           `json:"failed_required"`
	FailedOptional int           `json:"failed_optional"`
}

// PreflightOpts controls preflight.
type PreflightOpts struct {
	Base  string   // diff base, default origin/HEAD or origin/main
	Files []string // --files override: explicit changed file list
	All   bool     // run all matched checks even after a failure
	JSON  bool
	Root  string
}

// changedFiles resolves the diff file list: --files override or git diff.
func changedFiles(root, base string, files []string) ([]string, error) {
	if len(files) > 0 {
		return files, nil
	}
	if base == "" {
		base = "origin/HEAD"
		if _, err := git(root, "rev-parse", "--verify", "origin/HEAD"); err != nil {
			base = "origin/main"
		}
	}
	out, err := git(root, "diff", "--name-only", base+"...HEAD")
	if err != nil {
		out, err = git(root, "diff", "--name-only", base)
		if err != nil {
			return nil, fmt.Errorf("cannot diff against %s: %w", base, err)
		}
	}
	var list []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l != "" {
			list = append(list, l)
		}
	}
	return list, nil
}

func git(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	return cmd.Output()
}

// matchGlob is the local doublestar matcher (same semantics as docsync's).
func matchGlob(pattern, name string) bool {
	p := strings.Split(pattern, "/")
	n := strings.Split(name, "/")
	return matchSegs(p, n)
}

func matchSegs(p, n []string) bool {
	for len(p) > 0 {
		if p[0] == "**" {
			for i := 0; i <= len(n); i++ {
				if matchSegs(p[1:], n[i:]) {
					return true
				}
			}
			return false
		}
		if len(n) == 0 {
			return false
		}
		ok, err := path.Match(p[0], n[0])
		if err != nil || !ok {
			return false
		}
		p, n = p[1:], n[1:]
	}
	return len(n) == 0
}

// Preflight selects checks whose path globs match the diff and runs them.
// Exit contract: 0 all pass, 2 a required check failed.
func Preflight(cfg Config, opts PreflightOpts, out io.Writer) int {
	if opts.Root == "" {
		opts.Root = "."
	}
	if len(cfg.Checks) == 0 {
		fmt.Fprintln(out, "no preflight checks configured (.cix.yml)")
		return 0
	}
	files, err := changedFiles(opts.Root, opts.Base, opts.Files)
	if err != nil {
		fmt.Fprintf(out, "error: %v\n", err)
		return 5
	}
	res := PreflightResult{}
	for _, c := range cfg.Checks {
		matched := false
		for _, pat := range c.Paths {
			for _, f := range files {
				if matchGlob(pat, f) {
					matched = true
					break
				}
			}
			if matched {
				break
			}
		}
		if !matched {
			res.Checks = append(res.Checks, CheckResult{Name: c.Name, Matched: false,
				Command: strings.Join(c.Run, " && "), Passed: true, Optional: c.Optional})
			continue
		}
		cr := CheckResult{Name: c.Name, Matched: true,
			Command: strings.Join(c.Run, " && "), Optional: c.Optional}
		start := time.Now()
		var buf strings.Builder
		ok := true
		for _, cmdLine := range c.Run {
			cmd := exec.Command("sh", "-c", cmdLine)
			cmd.Dir = opts.Root
			cmd.Stdout = &buf
			cmd.Stderr = &buf
			if err := cmd.Run(); err != nil {
				ok = false
				fmt.Fprintf(&buf, "\nexit: %v\n", err)
				break
			}
		}
		cr.Passed = ok
		cr.Duration = int(time.Since(start).Seconds())
		s := strings.TrimSpace(buf.String())
		if lines := strings.Split(s, "\n"); len(lines) > 20 {
			s = strings.Join(lines[len(lines)-20:], "\n")
		}
		cr.Output = s
		res.Checks = append(res.Checks, cr)
		if !ok {
			if c.Optional {
				res.FailedOptional++
			} else {
				res.FailedRequired++
				res.Blocked = true
				if !opts.All {
					break // fail-fast
				}
			}
		}
	}
	if opts.JSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		_ = enc.Encode(res)
	} else {
		for _, c := range res.Checks {
			switch {
			case !c.Matched:
				fmt.Fprintf(out, "- %-24s skipped (no matching files)\n", c.Name)
			case c.Passed:
				fmt.Fprintf(out, "✓ %-24s %ds\n", c.Name, c.Duration)
			default:
				tag := ""
				if c.Optional {
					tag = " (optional)"
				}
				fmt.Fprintf(out, "✗ %-24s FAILED%s\n", c.Name, tag)
				if c.Output != "" {
					for _, l := range strings.Split(c.Output, "\n") {
						fmt.Fprintf(out, "  %s\n", l)
					}
				}
			}
		}
	}
	if res.Blocked {
		return 2
	}
	return 0
}
