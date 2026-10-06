package internal

import (
	"fmt"
	"io"
	"sort"
	"time"
)

// PushOpts controls cix push.
type PushOpts struct {
	Tag              string // push this tag instead of the branch
	CancelSuperseded bool
	Root             string
	Wait             WaitOpts // carried into the wait phase
	FindTimeout      time.Duration
}

// PushResult is the push phase's JSON output (before wait takes over).
type PushResult struct {
	Ref                 string  `json:"ref"`
	SHA                 string  `json:"sha"`
	Pushed              bool    `json:"pushed"`
	RunID               int64   `json:"run_id"`
	CancelledSuperseded []int64 `json:"cancelled_superseded"`
	SkippedCancel       bool    `json:"skipped_cancel"`
}

// Push runs git push, resolves the triggered run via head_sha (tags
// included), optionally cancels superseded runs, then hands off to Wait.
func Push(api API, cfg Config, opts PushOpts, out io.Writer) int {
	root := opts.Root
	if root == "" {
		root = "."
	}
	ref := "HEAD"
	if opts.Tag != "" {
		ref = "refs/tags/" + opts.Tag
	}
	sha, err := revParse(ref)
	if err != nil {
		fmt.Fprintf(out, "error: cannot resolve %s: %v\n", ref, err)
		return 5
	}

	args := []string{"push", "origin"}
	if opts.Tag != "" {
		args = append(args, "refs/tags/"+opts.Tag)
	} else {
		args = append(args, "HEAD")
	}
	if out2, err := git(root, args...); err != nil {
		fmt.Fprintf(out, "error: git push failed: %v\n%s\n", err, out2)
		return 5
	}

	// Find the run — poll until GitHub registers it.
	if opts.FindTimeout == 0 {
		opts.FindTimeout = 90 * time.Second
	}
	deadline := time.Now().Add(opts.FindTimeout)
	var run Run
	for {
		runs, err := api.ListRuns(sha)
		if err == nil && len(runs) > 0 {
			sort.Slice(runs, func(i, j int) bool { return runs[i].CreatedAt > runs[j].CreatedAt })
			run = runs[0]
			break
		}
		if time.Now().After(deadline) {
			fmt.Fprintf(out, "error: pushed %s but no workflow run appeared within %s\n",
				sha[:8], opts.FindTimeout)
			return 5
		}
		time.Sleep(3 * time.Second)
	}

	res := PushResult{Ref: opts.Tag, SHA: sha, Pushed: true, RunID: run.ID}
	if opts.Tag == "" {
		res.Ref = sha
	}

	// --cancel-superseded: only when the workflow lacks its own
	// concurrency group — don't fight GitHub's mechanism.
	if opts.CancelSuperseded {
		if wf := findWorkflow(Workflows(root), run); wf.Concurrency {
			res.SkippedCancel = true
		} else {
			older, _ := api.ListRuns(sha)
			for _, r := range older {
				if r.ID != run.ID && (r.Status == "queued" || r.Status == "in_progress") {
					if err := api.Cancel(r.ID); err == nil {
						res.CancelledSuperseded = append(res.CancelledSuperseded, r.ID)
					}
				}
			}
		}
	}

	fmt.Fprintf(out, "pushed %s — watching run %d\n", sha[:8], run.ID)
	opts.Wait.RunID = run.ID
	return Wait(api, cfg, opts.Wait, out)
}

// findWorkflow matches a run to its parsed workflow file.
func findWorkflow(wfs []Workflow, run Run) Workflow {
	for _, w := range wfs {
		if w.Name == run.Name || filepathBase(w.File) == filepathBase(run.Path) {
			return w
		}
	}
	return Workflow{}
}

// Workflows is LoadWorkflows bound to a root — small seam for push.
func Workflows(root string) []Workflow { return LoadWorkflows(root) }

func filepathBase(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == '\\' {
			return p[i+1:]
		}
	}
	return p
}
