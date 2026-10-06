package internal

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ArtifactResult is the release artifact gate verdict.
type ArtifactResult struct {
	Production   bool     `json:"production"`
	FailedChecks []string `json:"failed_checks"`
	CheckedJob   string   `json:"checked_job,omitempty"`
	CheckedFile  string   `json:"checked_file,omitempty"`
}

// ArtifactGate verifies a green "mobile"/release job actually produced a
// production artifact: workflow runs the right Gradle task, app.json
// doesn't point at a dev API, package.json has no forbidden deps, and any
// supplied .aab/.apk is signed for release. Returns nil when the gate does
// not apply (no artifact job ran, no Expo project, no artifact given).
func ArtifactGate(root string, wf Workflow, jobs []JobResult, cfg Config, artifactPaths []string) *ArtifactResult {
	// did an artifact-producing job run on this workflow?
	var jobName string
	for _, aj := range cfg.ArtifactJobs {
		for _, jr := range jobs {
			if BaseJobName(jr.Name) == aj {
				jobName = jr.Name
			}
		}
	}
	appJSON := findAppJSON(root)
	if jobName == "" && appJSON == "" && len(artifactPaths) == 0 {
		return nil
	}
	res := &ArtifactResult{Production: true, CheckedJob: jobName}

	// 1. workflow file: the artifact job must invoke the release task
	if jobName != "" && wf.File != "" {
		runs := jobRunSteps(wf.File, BaseJobName(jobName))
		joined := strings.Join(runs, "\n")
		for _, req := range cfg.Artifact.RequireGradleTasks {
			if !strings.Contains(joined, req) {
				res.FailedChecks = append(res.FailedChecks,
					fmt.Sprintf("workflow job %q never runs %s", jobName, req))
			}
		}
		for _, rej := range cfg.Artifact.RejectGradleTasks {
			if strings.Contains(joined, rej) {
				res.FailedChecks = append(res.FailedChecks,
					fmt.Sprintf("workflow job %q runs %s", jobName, rej))
			}
		}
	}

	// 2. app.json: API URL must not point at a dev host
	if appJSON != "" {
		res.CheckedFile = appJSON
		b, err := os.ReadFile(appJSON)
		if err == nil {
			var app struct {
				Expo map[string]any `json:"expo"`
			}
			if json.Unmarshal(b, &app) == nil && app.Expo != nil {
				url := findAPIURL(app.Expo)
				for _, bad := range cfg.Artifact.APIURLMustNotMatch {
					if strings.Contains(url, bad) {
						res.FailedChecks = append(res.FailedChecks,
							fmt.Sprintf("%s api url %q matches %q", appJSON, url, bad))
					}
				}
			}
		}
		// 3. package.json siblings: forbidden deps
		for dir := filepath.Dir(appJSON); ; dir = filepath.Dir(dir) {
			pj := filepath.Join(dir, "package.json")
			if b, err := os.ReadFile(pj); err == nil {
				var p struct {
					Dependencies    map[string]string `json:"dependencies"`
					DevDependencies map[string]string `json:"devDependencies"`
				}
				if json.Unmarshal(b, &p) == nil {
					for _, dep := range cfg.Artifact.ForbidDeps {
						if _, ok := p.Dependencies[dep]; ok {
							res.FailedChecks = append(res.FailedChecks,
								fmt.Sprintf("%s depends on %s", pj, dep))
						}
					}
				}
			}
			if dir == root || dir == "." || dir == "/" {
				break
			}
		}
	}

	// 4. artifact signing check
	for _, ap := range artifactPaths {
		out, err := exec.Command("jarsigner", "-verify", "-certs", ap).CombinedOutput()
		s := string(out)
		if err != nil {
			res.FailedChecks = append(res.FailedChecks,
				fmt.Sprintf("%s: jarsigner verify failed", ap))
		} else if strings.Contains(s, "CN=Android Debug") {
			res.FailedChecks = append(res.FailedChecks,
				fmt.Sprintf("%s is signed with the Android debug key", ap))
		}
	}

	if len(res.FailedChecks) > 0 {
		res.Production = false
	}
	return res
}

// jobRunSteps extracts every `run:` block of one job from a workflow file.
func jobRunSteps(wfPath, jobName string) []string {
	b, err := os.ReadFile(wfPath)
	if err != nil {
		return nil
	}
	var w struct {
		Jobs map[string]struct {
			Steps []struct {
				Run string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if yaml.Unmarshal(b, &w) != nil {
		return nil
	}
	var out []string
	for name, j := range w.Jobs {
		if name != jobName {
			continue
		}
		for _, s := range j.Steps {
			if s.Run != "" {
				out = append(out, s.Run)
			}
		}
	}
	return out
}

// findAppJSON locates an Expo app.json — checks apps/mobile first, then
// the repo root.
func findAppJSON(root string) string {
	for _, cand := range []string{
		filepath.Join(root, "apps", "mobile", "app.json"),
		filepath.Join(root, "app.json"),
	} {
		b, err := os.ReadFile(cand)
		if err != nil {
			continue
		}
		var probe struct {
			Expo map[string]any `json:"expo"`
		}
		if json.Unmarshal(b, &probe) == nil && probe.Expo != nil {
			return cand
		}
	}
	return ""
}

// findAPIURL digs apiUrl / API_URL / api_url out of expo.extra or top level.
func findAPIURL(expo map[string]any) string {
	if extra, ok := expo["extra"].(map[string]any); ok {
		for _, k := range []string{"apiUrl", "API_URL", "api_url", "apiURL"} {
			if s, ok := extra[k].(string); ok {
				return s
			}
		}
	}
	for _, k := range []string{"apiUrl", "API_URL", "api_url"} {
		if s, ok := expo[k].(string); ok {
			return s
		}
	}
	return ""
}
