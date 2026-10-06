package internal

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// JobDef is one job from a workflow file.
type JobDef struct {
	Needs   []string
	Outputs []string
}

// Workflow is a parsed .github/workflows file.
type Workflow struct {
	Name        string
	File        string // repo-relative path
	Jobs        map[string]JobDef
	Concurrency bool
}

type wfYAML struct {
	Name        string             `yaml:"name"`
	Jobs        map[string]jobYAML `yaml:"jobs"`
	Concurrency yaml.Node          `yaml:"concurrency"`
}

type jobYAML struct {
	Needs   any               `yaml:"needs"`
	Outputs map[string]string `yaml:"outputs"`
}

// ParseWorkflow loads one workflow file.
func ParseWorkflow(path string) (Workflow, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Workflow{}, err
	}
	var w wfYAML
	if err := yaml.Unmarshal(b, &w); err != nil {
		return Workflow{}, err
	}
	wf := Workflow{Name: w.Name, File: path, Jobs: map[string]JobDef{}}
	wf.Concurrency = !w.Concurrency.IsZero()
	for name, j := range w.Jobs {
		jd := JobDef{}
		switch n := j.Needs.(type) {
		case string:
			jd.Needs = []string{n}
		case []any:
			for _, v := range n {
				if s, ok := v.(string); ok {
					jd.Needs = append(jd.Needs, s)
				}
			}
		}
		for k := range j.Outputs {
			jd.Outputs = append(jd.Outputs, k)
		}
		sort.Strings(jd.Outputs)
		wf.Jobs[name] = jd
	}
	return wf, nil
}

// LoadWorkflows parses every workflow under .github/workflows/.
func LoadWorkflows(root string) []Workflow {
	var out []Workflow
	for _, dir := range []string{filepath.Join(root, ".github", "workflows")} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			ext := filepath.Ext(e.Name())
			if ext != ".yml" && ext != ".yaml" {
				continue
			}
			if wf, err := ParseWorkflow(filepath.Join(dir, e.Name())); err == nil {
				out = append(out, wf)
			}
		}
	}
	return out
}

// BaseJobName strips the matrix suffix from an API job name:
// "desktop (ubuntu-latest)" -> "desktop".
func BaseJobName(apiName string) string {
	if i := strings.Index(apiName, " ("); i > 0 {
		return apiName[:i]
	}
	return apiName
}

// DeployPath returns the set of jobs that gate the deploy path — the gate
// job plus everything it transitively needs. If no gate job exists, all
// jobs are on the path (conservative default).
func DeployPath(jobs map[string]JobDef, gate string) map[string]bool {
	onPath := map[string]bool{}
	// find gate job: explicit name, else "deploy", else "release"
	gateJob := ""
	if _, ok := jobs[gate]; gate != "" && ok {
		gateJob = gate
	}
	if gateJob == "" {
		for _, cand := range []string{"deploy", "release"} {
			if _, ok := jobs[cand]; ok {
				gateJob = cand
				break
			}
		}
	}
	if gateJob == "" {
		// No gate resolvable — including an empty jobs map when the
		// workflow file can't be parsed. nil means "everything gates".
		return nil
	}
	// walk needs backwards from the gate
	var visit func(string)
	visit = func(j string) {
		if onPath[j] {
			return
		}
		onPath[j] = true
		for _, dep := range jobs[j].Needs {
			visit(dep)
		}
	}
	visit(gateJob)
	return onPath
}

// JobOnPath maps an API job name (possibly matrix-suffixed) onto the
// workflow dependency map and reports whether it is on the deploy path.
// An empty/nil path is the conservative default: everything gates.
func JobOnPath(apiName string, path map[string]bool) bool {
	if len(path) == 0 {
		return true
	}
	return path[apiName] || path[BaseJobName(apiName)]
}
