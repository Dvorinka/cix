package internal

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"
)

// StatusResult is the cix status JSON contract — a one-shot snapshot.
type StatusResult struct {
	RunID      int64       `json:"run_id"`
	Status     string      `json:"status"`
	Conclusion string      `json:"conclusion"`
	URL        string      `json:"url,omitempty"`
	Jobs       []JobResult `json:"jobs"`
}

// Status takes a non-blocking snapshot of the newest run for a ref.
func Status(api API, cfg Config, opts WaitOpts, out io.Writer) int {
	opts.defaults()
	run, err := ResolveRun(api, opts)
	if err != nil {
		fmt.Fprintf(out, "error: %v\n", err)
		return 5
	}
	jobs, err := api.GetJobs(run.ID)
	if err != nil {
		fmt.Fprintf(out, "error: %v\n", err)
		return 5
	}
	hist := ReadHistoryFrom(opts.HistoryPath)
	stats := HistoryStats(hist)

	workflows := LoadWorkflows(opts.Root)
	var wf Workflow
	for _, w := range workflows {
		if w.Name == run.Name || filepath.Base(w.File) == filepath.Base(run.Path) {
			wf = w
			break
		}
	}
	deployPath := DeployPath(wf.Jobs, orDefault(opts.Gate, cfg.Gate))

	res := StatusResult{RunID: run.ID, Status: run.Status, Conclusion: run.Conclusion, URL: run.HTMLURL}
	for _, j := range jobs {
		jr := JobResult{
			Name:         j.Name,
			Status:       j.Status,
			Conclusion:   j.Conclusion,
			DurationSecs: j.Duration(),
			ElapsedSecs:  j.Duration(),
			P90Secs:      JobP90(stats, run.Name, j.Name),
			Gating:       JobOnPath(j.Name, deployPath),
			OnDeployPath: JobOnPath(j.Name, deployPath),
			FailedStep:   j.FailedStep(),
		}
		jr.Overage = jr.P90Secs > 0 && jr.ElapsedSecs > jr.P90Secs
		res.Jobs = append(res.Jobs, jr)
	}
	sort.Slice(res.Jobs, func(i, j int) bool { return res.Jobs[i].Name < res.Jobs[j].Name })

	if opts.JSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		_ = enc.Encode(res)
		return 0
	}
	fmt.Fprintf(out, "run %d — %s (%s)\n", res.RunID, orDash(res.Conclusion, res.Status), res.URL)
	for _, j := range res.Jobs {
		mark := "…"
		if j.Status == "completed" {
			mark = "✓"
			if failureConclusions[j.Conclusion] {
				mark = "✗"
			}
		}
		extra := ""
		if j.Overage {
			extra = fmt.Sprintf(" — longer than usual (p90 %s)", dur(j.P90Secs))
		}
		if j.FailedStep != "" {
			extra += " — " + j.FailedStep
		}
		fmt.Fprintf(out, "%s %-24s %-12s %s%s\n", mark, j.Name,
			orDash(j.Conclusion, j.Status), dur(j.DurationSecs), extra)
	}
	return 0
}

func orDash(a, b string) string {
	if a != "" {
		return a
	}
	if b != "" {
		return b
	}
	return "-"
}
