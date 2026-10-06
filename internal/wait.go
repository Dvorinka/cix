package internal

import (
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// JobResult is the per-job outcome in a wait/status report.
type JobResult struct {
	Name         string          `json:"name"`
	Conclusion   string          `json:"conclusion"`
	Status       string          `json:"status,omitempty"`
	DurationSecs int             `json:"duration_seconds"`
	ElapsedSecs  int             `json:"elapsed_seconds,omitempty"`
	P90Secs      int             `json:"p90_seconds,omitempty"`
	Overage      bool            `json:"overage,omitempty"`
	Gating       bool            `json:"gating"`
	OnDeployPath bool            `json:"on_deploy_path"`
	FailedStep   string          `json:"failed_step,omitempty"`
	LogSlice     string          `json:"log_slice,omitempty"`
	Signature    string          `json:"signature,omitempty"`
	Previous     *PrevOccurrence `json:"previous_occurrence,omitempty"`
}

// PrevOccurrence points at a prior run with the same failure signature.
type PrevOccurrence struct {
	RunID   int64  `json:"run_id"`
	HeadSHA string `json:"head_sha"`
	TS      string `json:"ts"`
}

// WaitResult is the cix wait/status JSON contract.
type WaitResult struct {
	RunID      int64               `json:"run_id"`
	Conclusion string              `json:"conclusion"`
	Status     string              `json:"status,omitempty"`
	Jobs       []JobResult         `json:"jobs"`
	FailedJob  string              `json:"failed_job"`
	FailedStep string              `json:"failed_step"`
	Gating     bool                `json:"gating"`
	LogSlice   string              `json:"log_slice"`
	Signature  string              `json:"signature"`
	Previous   *PrevOccurrence     `json:"previous_occurrence"`
	JobOutputs map[string][]string `json:"job_outputs,omitempty"`
	Artifact   *ArtifactResult     `json:"artifact,omitempty"`
	Verify     *VerifyResult       `json:"verify,omitempty"`
	URL        string              `json:"url,omitempty"`
}

// VerifyResult is the post-run verify hook outcome.
type VerifyResult struct {
	Command string `json:"command"`
	OK      bool   `json:"ok"`
	Output  string `json:"output,omitempty"`
}

// WaitOpts controls the wait loop.
type WaitOpts struct {
	RunID       int64
	Ref         string
	PR          int    // resolve the run for this PR's head commit
	Job         string // wait on a single gate job only
	Gate        string // deploy-path gate override
	Timeout     time.Duration
	RerunFlaky  bool
	Notify      bool
	Artifacts   []string // --artifact paths for the release gate
	JSON        bool
	Root        string        // repo root for workflow parsing
	Poll        time.Duration // base poll interval (default 10s; tests pass ~0)
	Sleep       func(time.Duration)
	HistoryPath string
}

func (o *WaitOpts) defaults() {
	if o.Timeout == 0 {
		o.Timeout = 45 * time.Minute
	}
	if o.Poll == 0 {
		o.Poll = 10 * time.Second
	}
	if o.Sleep == nil {
		o.Sleep = time.Sleep
	}
	if o.Root == "" {
		o.Root = "."
	}
	if o.HistoryPath == "" {
		o.HistoryPath = HistoryPath()
	}
}

// ResolveRun finds the target run: explicit id, or newest for ref/HEAD.
// head_sha is the reliable matcher — tag pushes don't surface under
// --branch filters. Falls back to ref-name matching for refs/tags/*.
func ResolveRun(api API, opts WaitOpts) (Run, error) {
	if opts.RunID != 0 {
		return api.GetRun(opts.RunID)
	}
	var runs []Run
	var err error
	if opts.PR != 0 {
		sha, ref, perr := api.PRHead(opts.PR)
		if perr != nil {
			return Run{}, fmt.Errorf("cannot resolve PR #%d: %w", opts.PR, perr)
		}
		if runs, err = api.ListRuns(sha); err != nil {
			return Run{}, err
		}
		if len(runs) == 0 && ref != "" {
			runs, err = api.ListRunsForRef(ref)
		}
		return newestRun(runs, fmt.Sprintf("PR #%d", opts.PR), err)
	}
	ref := opts.Ref
	if ref == "" {
		sha, shaErr := revParse("HEAD")
		if shaErr != nil {
			return Run{}, shaErr
		}
		runs, err = api.ListRuns(sha)
	} else {
		if sha, shaErr := revParse(ref); shaErr == nil {
			runs, err = api.ListRuns(sha)
		}
		if len(runs) == 0 {
			runs, err = api.ListRunsForRef(ref)
		}
	}
	return newestRun(runs, fmt.Sprintf("ref %q", ref), err)
}

func newestRun(runs []Run, what string, err error) (Run, error) {
	if err != nil {
		return Run{}, err
	}
	if len(runs) == 0 {
		return Run{}, fmt.Errorf("no workflow run found for %s", what)
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].CreatedAt > runs[j].CreatedAt })
	return runs[0], nil
}

func revParse(ref string) (string, error) {
	out, err := exec.Command("git", "rev-parse", ref).Output()
	if err != nil {
		return "", fmt.Errorf("cannot resolve %q: %w", ref, err)
	}
	return strings.TrimSpace(string(out)), nil
}

var failureConclusions = map[string]bool{
	"failure": true, "cancelled": true, "timed_out": true, "action_required": true,
}

// Wait is the core loop: poll jobs, exit early on gating failure, slice
// logs, honor the deploy path, run verify + artifact gates on success.
func Wait(api API, cfg Config, opts WaitOpts, out io.Writer) int {
	opts.defaults()
	run, err := ResolveRun(api, opts)
	if err != nil {
		fmt.Fprintf(out, "error: %v\n", err)
		return 5
	}

	// Workflow → deploy path.
	workflows := LoadWorkflows(opts.Root)
	var wf Workflow
	for _, w := range workflows {
		if w.File == run.Path || filepath.Base(w.File) == filepath.Base(run.Path) ||
			w.Name == run.Name {
			wf = w
			break
		}
	}
	deployPath := DeployPath(wf.Jobs, orDefault(opts.Gate, cfg.Gate))
	if opts.Job != "" {
		deployPath = map[string]bool{opts.Job: true, BaseJobName(opts.Job): true}
	}

	hist := ReadHistoryFrom(opts.HistoryPath)
	stats := HistoryStats(hist)

	deadline := time.Now().Add(opts.Timeout)
	interval := opts.Poll
	failedNonGating := map[string]Job{}
	var gatingFail *Job
	announcedOverage := map[string]bool{}
	var jobs []Job

	for {
		var jerr error
		jobs, jerr = pollJobs(api, run.ID)
		if jerr != nil {
			fmt.Fprintf(out, "error: %v\n", jerr)
			return 5
		}
		allDone := len(jobs) > 0
		if opts.Job != "" {
			// --job: done when the named job completes, not the whole run
			allDone = false
			for _, j := range jobs {
				if (j.Name == opts.Job || BaseJobName(j.Name) == opts.Job) &&
					j.Status == "completed" {
					allDone = true
				}
			}
		}
		for i := range jobs {
			j := &jobs[i]
			if j.Status != "completed" {
				if opts.Job == "" {
					allDone = false
				}
				continue
			}
			if !failureConclusions[j.Conclusion] {
				continue
			}
			if JobOnPath(j.Name, deployPath) {
				gatingFail = j
			} else {
				failedNonGating[j.Name] = *j
			}
		}

		// overage markers — informational only, once per job
		for _, j := range jobs {
			if j.Status == "in_progress" && j.StartedAt != "" && !announcedOverage[j.Name] {
				elapsed := j.Duration()
				if p90 := JobP90(stats, run.Name, j.Name); p90 > 0 && elapsed > p90 {
					announcedOverage[j.Name] = true
					fmt.Fprintf(out, "  %s — %s elapsed, longer than usual (p90 %s)\n",
						j.Name, dur(elapsed), dur(p90))
				}
			}
		}

		if gatingFail != nil {
			return finishFailure(api, cfg, run, wf, jobs, *gatingFail, deployPath, hist, opts, out)
		}
		if allDone {
			break
		}
		if time.Now().After(deadline) {
			fmt.Fprintf(out, "timeout after %s\n", opts.Timeout)
			return 4
		}
		var jitter time.Duration
		if n := int64(interval) / 5; n > 0 {
			jitter = time.Duration(rand.Int63n(n))
		}
		opts.Sleep(interval + jitter)
		interval = nextBackoff(interval)
	}

	res := buildResult(run, wf, jobs, deployPath, stats)
	exitCode := 0
	if len(failedNonGating) > 0 {
		exitCode = 1 // non-gating failures: warnings, deploy path still green
	}
	if cfg.Verify != "" && exitCode == 0 {
		vr := runVerify(cfg.Verify)
		res.Verify = vr
		if !vr.OK {
			exitCode = 3
		}
	}
	if exitCode == 0 {
		res.Artifact = ArtifactGate(opts.Root, wf, res.Jobs, cfg, opts.Artifacts)
		if res.Artifact != nil && !res.Artifact.Production {
			exitCode = 3
		}
	}

	recordHistory(opts.HistoryPath, run, jobs)
	emitResult(res, opts.JSON, out)
	notify(opts.Notify, exitCode, run)
	return exitCode
}

func pollJobs(api API, runID int64) ([]Job, error) {
	var last error
	for i := 0; i < 3; i++ {
		jobs, err := api.GetJobs(runID)
		if err == nil {
			return jobs, nil
		}
		last = err
		time.Sleep(5 * time.Second)
	}
	return nil, last
}

func nextBackoff(d time.Duration) time.Duration {
	switch {
	case d < 20*time.Second:
		return 20 * time.Second
	case d < 30*time.Second:
		return 30 * time.Second
	default:
		return 60 * time.Second
	}
}

// finishFailure handles a gating job failure: fetch log, slice, signature,
// previous occurrence, emit, record history, exit 2.
func finishFailure(api API, cfg Config, run Run, wf Workflow, jobs []Job, j Job,
	deployPath map[string]bool, hist []HistoryRecord, opts WaitOpts, out io.Writer) int {

	failedStep := j.FailedStep()
	slice, sig := "", ""
	if l, err := api.JobLog(j.ID); err == nil {
		slice = SliceLog(l, failedStep)
		sig = SignatureWith(slice, cfg.Signatures)
	}
	if opts.RerunFlaky && slice != "" && IsTransient(slice) {
		if err := api.RerunFailed(run.ID); err == nil {
			fmt.Fprintf(out, "%s — transient failure, rerunning failed jobs once\n", j.Name)
			AppendHistoryTo(opts.HistoryPath, HistoryRecord{
				RunID: run.ID, Workflow: run.Name, Job: j.Name,
				Conclusion: j.Conclusion, DurationSeconds: j.Duration(),
				HeadSHA: shortSHA(run.HeadSHA), TS: run.CreatedAt,
				Signature: sig, FailedStep: failedStep})
			opts.RerunFlaky = false // one-shot retry
			return Wait(api, cfg, opts, out)
		}
	}

	var prev *PrevOccurrence
	if sig != "" {
		if p := PreviousOccurrence(hist, sig, run.ID); p != nil {
			prev = &PrevOccurrence{RunID: p.RunID, HeadSHA: p.HeadSHA, TS: p.TS}
		}
	}

	res := buildResult(run, wf, jobs, deployPath, HistoryStats(hist))
	res.Conclusion = j.Conclusion
	res.FailedJob = j.Name
	res.FailedStep = failedStep
	res.Gating = true
	res.LogSlice = slice
	res.Signature = sig
	res.Previous = prev
	for i := range res.Jobs {
		if res.Jobs[i].Name == j.Name {
			res.Jobs[i].FailedStep = failedStep
			res.Jobs[i].LogSlice = slice
			res.Jobs[i].Signature = sig
			res.Jobs[i].Previous = prev
		}
	}

	// history: all jobs + signature on the failed one
	recordHistory(opts.HistoryPath, run, jobs)
	if sig != "" {
		AppendHistoryTo(opts.HistoryPath, HistoryRecord{
			RunID: run.ID, Workflow: run.Name, Job: BaseJobName(j.Name),
			Conclusion: j.Conclusion, DurationSeconds: j.Duration(),
			HeadSHA: shortSHA(run.HeadSHA), TS: run.CreatedAt,
			Signature: sig, FailedStep: failedStep})
	}
	emitResult(res, opts.JSON, out)
	notify(opts.Notify, 2, run)
	return 2
}

func buildResult(run Run, wf Workflow, jobs []Job, deployPath map[string]bool,
	stats []JobStat) WaitResult {
	res := WaitResult{RunID: run.ID, Conclusion: "success", URL: run.HTMLURL,
		JobOutputs: map[string][]string{}}
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
		if failureConclusions[j.Conclusion] && jr.Gating {
			res.Conclusion = "failure"
		}
		res.Jobs = append(res.Jobs, jr)
	}
	for name, jd := range wf.Jobs {
		if len(jd.Outputs) > 0 {
			res.JobOutputs[name] = jd.Outputs
		}
	}
	return res
}

func recordHistory(path string, run Run, jobs []Job) {
	for _, j := range jobs {
		AppendHistoryTo(path, HistoryRecord{
			RunID: run.ID, Workflow: run.Name, Job: BaseJobName(j.Name),
			Conclusion: j.Conclusion, DurationSeconds: j.Duration(),
			HeadSHA: shortSHA(run.HeadSHA), TS: run.CreatedAt,
		})
	}
}

func shortSHA(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

func runVerify(cmd string) *VerifyResult {
	out, err := exec.Command("sh", "-c", cmd).CombinedOutput()
	res := &VerifyResult{Command: cmd, OK: err == nil}
	s := strings.TrimSpace(string(out))
	if len(s) > 500 {
		s = s[len(s)-500:]
	}
	res.Output = s
	return res
}

func emitResult(res WaitResult, asJSON bool, out io.Writer) {
	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		_ = enc.Encode(res)
		return
	}
	for _, j := range res.Jobs {
		mark := "✓"
		if failureConclusions[j.Conclusion] {
			mark = "✗"
		} else if j.Conclusion == "skipped" {
			mark = "-"
		}
		tag := ""
		if !j.Gating && j.Conclusion != "success" && j.Conclusion != "" {
			tag = " (non-blocking)"
		}
		line := fmt.Sprintf("%s %s — %s", mark, j.Name, dur(j.DurationSecs))
		if j.FailedStep != "" {
			line += " — " + j.FailedStep
		}
		fmt.Fprintln(out, line+tag)
		if j.LogSlice != "" {
			for _, l := range strings.Split(j.LogSlice, "\n") {
				fmt.Fprintln(out, "  "+l)
			}
		}
		if j.Signature != "" {
			fmt.Fprintf(out, "  signature: %s\n", j.Signature)
		}
		if j.Previous != nil {
			fmt.Fprintf(out, "  previous occurrence: run %d (%s), commit %s\n",
				j.Previous.RunID, j.Previous.TS, j.Previous.HeadSHA)
		}
	}
	var names []string
	for job := range res.JobOutputs {
		names = append(names, job)
	}
	sort.Strings(names)
	for _, job := range names {
		fmt.Fprintf(out, "  outputs %s: %s\n", job, strings.Join(res.JobOutputs[job], ", "))
	}
	if res.Verify != nil {
		if res.Verify.OK {
			fmt.Fprintf(out, "verify: ok — %s\n", res.Verify.Command)
		} else {
			fmt.Fprintf(out, "verify: FAILED — %s\n%s\n", res.Verify.Command, res.Verify.Output)
		}
	}
	if res.Artifact != nil {
		if res.Artifact.Production {
			fmt.Fprintln(out, "artifact: production build confirmed")
		} else {
			fmt.Fprintf(out, "artifact: NOT a production build — %v\n", res.Artifact.FailedChecks)
		}
	}
}

func notify(enabled bool, code int, run Run) {
	if !enabled {
		return
	}
	msg := "CI finished — " + run.Name
	switch code {
	case 0:
		msg = "CI passed — " + run.Name
	case 1:
		msg = "CI warnings — " + run.Name
	default:
		msg = fmt.Sprintf("CI failed — %s (exit %d)", run.Name, code)
	}
	if err := exec.Command("notify-send", "cix", msg).Run(); err != nil {
		// macOS fallback; best-effort, ignore errors
		_ = exec.Command("osascript", "-e",
			fmt.Sprintf(`display notification "%s" with title "cix"`, msg)).Run()
	}
}

func dur(s int) string {
	if s <= 0 {
		return "0s"
	}
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	return fmt.Sprintf("%dm%02ds", s/60, s%60)
}

func orDefault(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
