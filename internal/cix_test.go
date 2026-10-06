package internal

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func mustRead(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// ---------- workflow parsing ----------

func TestParseWorkflow(t *testing.T) {
	wf, err := ParseWorkflow(filepath.Join("..", "testdata", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if wf.Name != "release" {
		t.Fatalf("name = %q", wf.Name)
	}
	if !wf.Concurrency {
		t.Fatal("concurrency not detected")
	}
	// needs as string
	if got := wf.Jobs["test-api"].Needs; len(got) != 1 || got[0] != "lint" {
		t.Fatalf("test-api needs = %v", got)
	}
	// needs as list
	if got := wf.Jobs["mobile"].Needs; len(got) != 2 {
		t.Fatalf("mobile needs = %v", got)
	}
	// outputs
	if got := wf.Jobs["mobile"].Outputs; len(got) != 2 {
		t.Fatalf("mobile outputs = %v", got)
	}
}

func TestDeployPath(t *testing.T) {
	wf, err := ParseWorkflow(filepath.Join("..", "testdata", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	path := DeployPath(wf.Jobs, "")
	for _, want := range []string{"deploy", "test-api", "desktop", "mobile", "lint"} {
		if !path[want] {
			t.Fatalf("deploy path missing %q", want)
		}
	}
	// gate override
	path = DeployPath(wf.Jobs, "mobile")
	if !path["mobile"] || !path["lint"] || !path["test-api"] {
		t.Fatalf("mobile path = %v", path)
	}
	if path["desktop"] || path["deploy"] {
		t.Fatalf("mobile path should not include desktop/deploy: %v", path)
	}
	// no gate → nil path → everything gates
	noGate := map[string]JobDef{"a": {}, "b": {Needs: []string{"a"}}}
	path = DeployPath(noGate, "")
	if !JobOnPath("a", path) || !JobOnPath("b", path) {
		t.Fatal("no gate job → all jobs gating")
	}
	// unparseable workflow (no jobs) → also all-gating
	if !JobOnPath("anything", nil) {
		t.Fatal("nil path must be conservative")
	}
}

func TestBaseJobName(t *testing.T) {
	if got := BaseJobName("desktop (ubuntu-latest)"); got != "desktop" {
		t.Fatalf("got %q", got)
	}
	if got := BaseJobName("lint"); got != "lint" {
		t.Fatalf("got %q", got)
	}
}

// ---------- log slicing + signatures ----------

func TestSliceGradle(t *testing.T) {
	log := mustRead(t, filepath.Join("..", "testdata", "logs", "gradle-failure.log"))
	slice := SliceLog(log, "build")
	if !strings.Contains(slice, "Unresolved reference") {
		t.Fatalf("slice missing kotlin errors:\n%s", slice)
	}
	sig := Signature(slice)
	if !strings.HasPrefix(sig, "kotlin") && !strings.HasPrefix(sig, "gradle") {
		t.Fatalf("sig = %q", sig)
	}
	if IsTransient(slice) {
		t.Fatal("compile failure marked transient")
	}
}

func TestSliceMetro(t *testing.T) {
	log := mustRead(t, filepath.Join("..", "testdata", "logs", "metro-failure.log"))
	slice := SliceLog(log, "")
	sig := Signature(slice)
	if sig != "metro/module-not-found" {
		t.Fatalf("sig = %q", sig)
	}
}

func TestSliceGoTest(t *testing.T) {
	log := mustRead(t, filepath.Join("..", "testdata", "logs", "gotest-failure.log"))
	slice := SliceLog(log, "")
	sig := Signature(slice)
	if sig != "gotest/TestSendMessage" {
		t.Fatalf("sig = %q", sig)
	}
}

func TestNormalizeLine(t *testing.T) {
	in := "2025-01-15T10:30:00.123Z Error at /home/user/proj/src/x.go:42:10 sha a1b2c3d4e5f6 v1.2.3"
	got := NormalizeLine(in)
	if strings.Contains(got, "a1b2c3d4") || strings.Contains(got, "42") {
		t.Fatalf("not normalized: %q", got)
	}
}

func TestIsTransient(t *testing.T) {
	if !IsTransient("The runner lost contact with the server") {
		t.Fatal("runner loss should be transient")
	}
	if IsTransient("--- FAIL: TestX\nFAIL") {
		t.Fatal("test failure is not transient")
	}
}

// ---------- history ----------

func TestHistoryStats(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "history.jsonl")
	recs := []HistoryRecord{
		{RunID: 1, Workflow: "ci", Job: "test", Conclusion: "success", DurationSeconds: 100, TS: "2024-01-01T00:00:00Z"},
		{RunID: 2, Workflow: "ci", Job: "test", Conclusion: "success", DurationSeconds: 200, TS: "2024-01-02T00:00:00Z"},
		{RunID: 3, Workflow: "ci", Job: "test", Conclusion: "failure", DurationSeconds: 300, TS: "2024-01-03T00:00:00Z", Signature: "gotest/TestX"},
		{RunID: 3, Workflow: "ci", Job: "lint", Conclusion: "success", DurationSeconds: 30, TS: "2024-01-03T00:00:00Z"},
	}
	for _, r := range recs {
		if err := AppendHistoryTo(p, r); err != nil {
			t.Fatal(err)
		}
	}
	read := ReadHistoryFrom(p)
	if len(read) != 4 {
		t.Fatalf("read %d records", len(read))
	}
	stats := HistoryStats(read)
	if len(stats) != 2 {
		t.Fatalf("stats = %v", stats)
	}
	var test JobStat
	for _, s := range stats {
		if s.Job == "test" {
			test = s
		}
	}
	if test.Count != 3 || test.P50Seconds != 200 || test.P90Seconds != 300 {
		t.Fatalf("test stat = %+v", test)
	}
	if test.LastConclusion != "failure" {
		t.Fatalf("last conclusion = %q", test.LastConclusion)
	}
	prev := PreviousOccurrence(read, "gotest/TestX", 9)
	if prev == nil || prev.RunID != 3 {
		t.Fatalf("prev = %v", prev)
	}
	if PreviousOccurrence(read, "gotest/TestX", 3) != nil {
		t.Fatal("excludeRunID not honored")
	}
}

// ---------- wait loop with fake API ----------

type fakeAPI struct {
	runs      []Run
	jobPages  [][]Job // successive GetJobs responses
	page      int
	logs      map[int64]string
	cancelled []int64
	reran     bool
}

func (f *fakeAPI) ListRuns(sha string) ([]Run, error)       { return f.runs, nil }
func (f *fakeAPI) ListRunsForRef(ref string) ([]Run, error) { return f.runs, nil }
func (f *fakeAPI) GetRun(id int64) (Run, error) {
	for _, r := range f.runs {
		if r.ID == id {
			return r, nil
		}
	}
	return f.runs[0], nil
}
func (f *fakeAPI) GetJobs(id int64) ([]Job, error) {
	if f.page < len(f.jobPages) {
		p := f.jobPages[f.page]
		f.page++
		return p, nil
	}
	return f.jobPages[len(f.jobPages)-1], nil
}
func (f *fakeAPI) JobLog(jobID int64) (string, error) { return f.logs[jobID], nil }
func (f *fakeAPI) Cancel(id int64) error              { f.cancelled = append(f.cancelled, id); return nil }
func (f *fakeAPI) RerunFailed(id int64) error         { f.reran = true; return nil }

func waitOpts(t *testing.T) WaitOpts {
	return WaitOpts{
		RunID:       42,
		Poll:        time.Nanosecond,
		Sleep:       func(time.Duration) {},
		Timeout:     time.Minute,
		HistoryPath: filepath.Join(t.TempDir(), "h.jsonl"),
		Root:        t.TempDir(),
	}
}

func baseRun() Run {
	return Run{ID: 42, Name: "release", HeadSHA: "abc123",
		Path: ".github/workflows/release.yml", CreatedAt: "2024-05-01T10:00:00Z"}
}

func job(name, status, concl string) Job {
	return Job{Name: name, Status: status, Conclusion: concl,
		StartedAt: "2024-05-01T10:00:00Z", CompletedAt: "2024-05-01T10:02:00Z",
		Steps: []Step{{Name: "build", Conclusion: concl, Number: 1}}}
}

func TestWaitSuccess(t *testing.T) {
	api := &fakeAPI{runs: []Run{baseRun()}, logs: map[int64]string{},
		jobPages: [][]Job{
			{job("lint", "in_progress", ""), job("test-api", "queued", "")},
			{job("lint", "completed", "success"), job("test-api", "completed", "success")},
		}}
	var out bytes.Buffer
	code := Wait(api, DefaultConfig(), waitOpts(t), &out)
	if code != 0 {
		t.Fatalf("code=%d out=%s", code, out.String())
	}
}

func TestWaitGatingFailure(t *testing.T) {
	// no workflow file → conservative: all jobs gating
	log := mustRead(t, filepath.Join("..", "testdata", "logs", "gotest-failure.log"))
	api := &fakeAPI{runs: []Run{baseRun()},
		logs: map[int64]string{2: log},
		jobPages: [][]Job{
			{job("lint", "completed", "success"),
				{Name: "test-api", ID: 2, Status: "completed", Conclusion: "failure",
					StartedAt: "2024-05-01T10:00:00Z", CompletedAt: "2024-05-01T10:04:00Z",
					Steps: []Step{
						{Name: "checkout", Conclusion: "success", Number: 1},
						{Name: "go test", Conclusion: "failure", Number: 2},
					}}},
		}}
	var out bytes.Buffer
	opts := waitOpts(t)
	opts.JSON = true
	code := Wait(api, DefaultConfig(), opts, &out)
	if code != 2 {
		t.Fatalf("code=%d out=%s", code, out.String())
	}
	var res WaitResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("bad json: %v\n%s", err, out.String())
	}
	if res.FailedJob != "test-api" || res.FailedStep != "go test" {
		t.Fatalf("res = %+v", res)
	}
	if !strings.HasPrefix(res.Signature, "gotest/") {
		t.Fatalf("signature = %q", res.Signature)
	}
	if res.LogSlice == "" {
		t.Fatal("empty log slice")
	}
}

func TestWaitNonGatingFailure(t *testing.T) {
	// workflow with deploy gate: mobile fails but isn't needed by deploy
	root := t.TempDir()
	wfDir := filepath.Join(root, ".github", "workflows")
	os.MkdirAll(wfDir, 0o755)
	os.WriteFile(filepath.Join(wfDir, "release.yml"), []byte(`name: release
jobs:
  lint: {}
  mobile:
    needs: lint
  deploy:
    needs: lint
`), 0o644)

	api := &fakeAPI{runs: []Run{baseRun()}, logs: map[int64]string{},
		jobPages: [][]Job{
			{job("lint", "completed", "success"),
				job("mobile", "completed", "failure"),
				job("deploy", "completed", "success")},
		}}
	var out bytes.Buffer
	opts := waitOpts(t)
	opts.Root = root
	code := Wait(api, DefaultConfig(), opts, &out)
	if code != 1 {
		t.Fatalf("code=%d out=%s", code, out.String())
	}
	if !strings.Contains(out.String(), "non-blocking") {
		t.Fatalf("missing non-blocking tag:\n%s", out.String())
	}
}

func TestWaitTimeout(t *testing.T) {
	api := &fakeAPI{runs: []Run{baseRun()}, logs: map[int64]string{},
		jobPages: [][]Job{
			{job("lint", "in_progress", "")},
		}}
	var out bytes.Buffer
	opts := waitOpts(t)
	opts.Timeout = time.Millisecond // expires after first poll
	code := Wait(api, DefaultConfig(), opts, &out)
	if code != 4 {
		t.Fatalf("code=%d", code)
	}
}

// ---------- artifact gate ----------

func TestArtifactGate(t *testing.T) {
	root := t.TempDir()
	// expo app with dev API url
	mobile := filepath.Join(root, "apps", "mobile")
	os.MkdirAll(mobile, 0o755)
	os.WriteFile(filepath.Join(mobile, "app.json"), []byte(`{
  "expo": {"name": "relay", "extra": {"apiUrl": "http://10.0.2.2:8080"}}
}`), 0o644)
	os.WriteFile(filepath.Join(mobile, "package.json"), []byte(`{
  "dependencies": {"expo": "51.0.0", "expo-dev-client": "4.0.0"}
}`), 0o644)

	res := ArtifactGate(root, Workflow{}, nil, DefaultConfig(), nil)
	if res == nil {
		t.Fatal("gate did not apply")
	}
	if res.Production {
		t.Fatalf("should have failed: %+v", res)
	}
	if len(res.FailedChecks) < 2 {
		t.Fatalf("expected ≥2 failed checks (api url + dep), got %v", res.FailedChecks)
	}

	// clean project → passes
	root2 := t.TempDir()
	mobile2 := filepath.Join(root2, "apps", "mobile")
	os.MkdirAll(mobile2, 0o755)
	os.WriteFile(filepath.Join(mobile2, "app.json"), []byte(`{
  "expo": {"name": "relay", "extra": {"apiUrl": "https://api.relay.dev"}}
}`), 0o644)
	os.WriteFile(filepath.Join(mobile2, "package.json"), []byte(`{
  "dependencies": {"expo": "51.0.0"}
}`), 0o644)
	res = ArtifactGate(root2, Workflow{}, nil, DefaultConfig(), nil)
	if res == nil || !res.Production {
		t.Fatalf("clean project failed gate: %+v", res)
	}
}

// ---------- preflight ----------

func TestPreflight(t *testing.T) {
	root := t.TempDir()
	cfg := DefaultConfig()
	cfg.Checks = []PreflightCheck{
		{Name: "go", Paths: []string{"**/*.go"}, Run: []string{"echo go-ok"}},
		{Name: "ts", Paths: []string{"web/**"}, Run: []string{"exit 1"}},
		{Name: "optional", Paths: []string{"docs/**"}, Run: []string{"exit 1"}, Optional: true},
	}
	var out bytes.Buffer
	code := Preflight(cfg, PreflightOpts{
		Files: []string{"main.go", "docs/readme.md"}, Root: root}, &out)
	if code != 0 {
		t.Fatalf("code=%d out=%s", code, out.String())
	}
	out.Reset()
	code = Preflight(cfg, PreflightOpts{
		Files: []string{"web/app.ts"}, Root: root}, &out)
	if code != 2 {
		t.Fatalf("code=%d out=%s", code, out.String())
	}
}
