package internal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Run is a GitHub Actions workflow run.
type Run struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	HeadSHA    string `json:"head_sha"`
	HeadBranch string `json:"head_branch"`
	Event      string `json:"event"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	CreatedAt  string `json:"created_at"`
	HTMLURL    string `json:"html_url"`
	Path       string `json:"path"` // .github/workflows/release.yml
}

// Step is one step inside a job.
type Step struct {
	Name       string `json:"name"`
	Conclusion string `json:"conclusion"`
	Number     int    `json:"number"`
}

// Job is one job inside a run.
type Job struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	Conclusion  string `json:"conclusion"`
	StartedAt   string `json:"started_at"`
	CompletedAt string `json:"completed_at"`
	Steps       []Step `json:"steps"`
}

// FailedStep returns the first step with conclusion "failure".
func (j Job) FailedStep() string {
	for _, s := range j.Steps {
		if s.Conclusion == "failure" {
			return s.Name
		}
	}
	return ""
}

// Duration computes job wall-clock seconds from started/completed.
func (j Job) Duration() int {
	if j.StartedAt == "" {
		return 0
	}
	start, err := time.Parse(time.RFC3339, j.StartedAt)
	if err != nil {
		return 0
	}
	end := time.Now().UTC()
	if j.CompletedAt != "" {
		if t, err := time.Parse(time.RFC3339, j.CompletedAt); err == nil {
			end = t
		}
	}
	return int(end.Sub(start).Seconds())
}

// API abstracts GitHub access so wait/status can be tested with fakes.
type API interface {
	ListRuns(headSHA string) ([]Run, error)
	ListRunsForRef(ref string) ([]Run, error)
	GetRun(runID int64) (Run, error)
	GetJobs(runID int64) ([]Job, error)
	JobLog(jobID int64) (string, error)
	Cancel(runID int64) error
	RerunFailed(runID int64) error
	PRHead(pr int) (sha, ref string, err error)
}

// GH wraps the gh CLI — all GitHub access delegates to it.
type GH struct {
	Repo string // "owner/repo"
	Run  func(args ...string) ([]byte, error)
}

// NewGH detects the repo from the git remote via gh.
func NewGH() (*GH, error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return nil, fmt.Errorf("`gh` CLI not found. Install from https://cli.github.com/")
	}
	g := &GH{Run: execGH}
	out, err := g.Run("repo", "view", "--json", "nameWithOwner", "-q", ".nameWithOwner")
	if err != nil {
		return nil, fmt.Errorf("`gh` could not resolve the repo — are you in a checkout with a GitHub remote? %v", err)
	}
	g.Repo = strings.TrimSpace(string(out))
	return g, nil
}

func execGH(args ...string) ([]byte, error) {
	cmd := exec.Command("gh", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("gh %s: %s", strings.Join(args, " "), strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

func (g *GH) api(path string) ([]byte, error) {
	return g.Run("api", path, "-H", "Accept: application/vnd.github+json")
}

func (g *GH) apiPost(path string) error {
	_, err := g.Run("api", "-X", "POST", path)
	return err
}

// ListRuns returns runs filtered by head_sha — the reliable matcher for
// both branch and tag pushes.
func (g *GH) ListRuns(headSHA string) ([]Run, error) {
	out, err := g.api(fmt.Sprintf("repos/%s/actions/runs?head_sha=%s", g.Repo, headSHA))
	if err != nil {
		return nil, err
	}
	var res struct {
		Runs []Run `json:"workflow_runs"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return nil, err
	}
	return res.Runs, nil
}

// ListRunsForRef returns runs for a branch/tag name — fallback when the
// sha isn't known (e.g. remote-only refs).
func (g *GH) ListRunsForRef(ref string) ([]Run, error) {
	out, err := g.api(fmt.Sprintf("repos/%s/actions/runs?branch=%s", g.Repo, ref))
	if err != nil {
		return nil, err
	}
	var res struct {
		Runs []Run `json:"workflow_runs"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return nil, err
	}
	return res.Runs, nil
}

// GetRun fetches a single run.
func (g *GH) GetRun(runID int64) (Run, error) {
	var r Run
	out, err := g.api(fmt.Sprintf("repos/%s/actions/runs/%d", g.Repo, runID))
	if err != nil {
		return r, err
	}
	return r, json.Unmarshal(out, &r)
}

// GetJobs fetches the jobs of a run.
func (g *GH) GetJobs(runID int64) ([]Job, error) {
	out, err := g.api(fmt.Sprintf("repos/%s/actions/runs/%d/jobs?per_page=100", g.Repo, runID))
	if err != nil {
		return nil, err
	}
	var res struct {
		Jobs []Job `json:"jobs"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return nil, err
	}
	return res.Jobs, nil
}

// JobLog fetches a completed job's raw log. Only exists post-completion —
// callers must check job status first.
func (g *GH) JobLog(jobID int64) (string, error) {
	out, err := g.api(fmt.Sprintf("repos/%s/actions/jobs/%d/logs", g.Repo, jobID))
	return string(out), err
}

// Cancel cancels a run.
func (g *GH) Cancel(runID int64) error {
	return g.apiPost(fmt.Sprintf("repos/%s/actions/runs/%d/cancel", g.Repo, runID))
}

// RerunFailed re-runs only the failed jobs of a run.
func (g *GH) RerunFailed(runID int64) error {
	return g.apiPost(fmt.Sprintf("repos/%s/actions/runs/%d/rerun-failed-jobs", g.Repo, runID))
}

// PRHead resolves a PR's head sha and branch — `cix wait --pr N` wants the
// run for the PR's head commit, not the repo's HEAD.
func (g *GH) PRHead(pr int) (string, string, error) {
	out, err := g.api(fmt.Sprintf("repos/%s/pulls/%d", g.Repo, pr))
	if err != nil {
		return "", "", err
	}
	var res struct {
		Head struct {
			SHA string `json:"sha"`
			Ref string `json:"ref"`
		} `json:"head"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return "", "", err
	}
	return res.Head.SHA, res.Head.Ref, nil
}
