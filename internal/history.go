package internal

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
)

// HistoryRecord is one observed CI job outcome, appended to history.jsonl.
type HistoryRecord struct {
	RunID           int64  `json:"run_id"`
	Workflow        string `json:"workflow"`
	Job             string `json:"job"`
	Conclusion      string `json:"conclusion"`
	DurationSeconds int    `json:"duration_seconds"`
	HeadSHA         string `json:"head_sha"`
	TS              string `json:"ts"`
	Signature       string `json:"signature,omitempty"`
	FailedStep      string `json:"failed_step,omitempty"`
}

// JobStat aggregates history for one workflow+job pair.
type JobStat struct {
	Workflow       string `json:"workflow"`
	Job            string `json:"job"`
	Count          int    `json:"count"`
	P50Seconds     int    `json:"p50_seconds"`
	P90Seconds     int    `json:"p90_seconds"`
	LastConclusion string `json:"last_conclusion"`
	LastRunID      int64  `json:"last_run_id"`
	LastTS         string `json:"last_ts"`
}

// HistoryPath returns ${XDG_DATA_HOME:-~/.local/share}/cix/history.jsonl.
func HistoryPath() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "cix", "history.jsonl")
}

// AppendHistory writes one JSON line to the history file.
func AppendHistory(rec HistoryRecord) error {
	return AppendHistoryTo(HistoryPath(), rec)
}

// AppendHistoryTo writes to an explicit path (tests).
func AppendHistoryTo(path string, rec HistoryRecord) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	return err
}

// ReadHistory loads all records; corrupt lines are skipped.
func ReadHistory() []HistoryRecord {
	return ReadHistoryFrom(HistoryPath())
}

// ReadHistoryFrom reads an explicit history file.
func ReadHistoryFrom(path string) []HistoryRecord {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []HistoryRecord
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var r HistoryRecord
		if json.Unmarshal(line, &r) == nil && r.RunID != 0 {
			out = append(out, r)
		}
	}
	return out
}

// HistoryStats aggregates per workflow+job: count, p50/p90, last outcome.
func HistoryStats(recs []HistoryRecord) []JobStat {
	type key struct{ wf, job string }
	durs := map[key][]int{}
	last := map[key]HistoryRecord{}
	for _, r := range recs {
		k := key{r.Workflow, r.Job}
		if r.DurationSeconds > 0 {
			durs[k] = append(durs[k], r.DurationSeconds)
		}
		if r.TS >= last[k].TS {
			last[k] = r
		}
	}
	var out []JobStat
	for k, ds := range durs {
		sort.Ints(ds)
		st := JobStat{
			Workflow:       k.wf,
			Job:            k.job,
			Count:          len(ds),
			P50Seconds:     percentile(ds, 50),
			P90Seconds:     percentile(ds, 90),
			LastConclusion: last[k].Conclusion,
			LastRunID:      last[k].RunID,
			LastTS:         last[k].TS,
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Workflow != out[j].Workflow {
			return out[i].Workflow < out[j].Workflow
		}
		return out[i].Job < out[j].Job
	})
	return out
}

func percentile(sorted []int, p int) int {
	if len(sorted) == 0 {
		return 0
	}
	// nearest-rank: ceil(p/100 * n) - 1
	idx := (p*len(sorted)+99)/100 - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// PreviousOccurrence finds the most recent prior run with the same
// failure signature, excluding the current run.
func PreviousOccurrence(recs []HistoryRecord, signature string, excludeRunID int64) *HistoryRecord {
	var best *HistoryRecord
	for i := range recs {
		r := &recs[i]
		if r.Signature == signature && r.RunID != excludeRunID {
			if best == nil || r.TS > best.TS {
				best = r
			}
		}
	}
	return best
}

// JobP90 returns the historical p90 duration for a job (0 if unknown).
func JobP90(stats []JobStat, workflow, job string) int {
	base := BaseJobName(job)
	for _, s := range stats {
		if s.Workflow == workflow && (s.Job == job || s.Job == base) {
			return s.P90Seconds
		}
	}
	return 0
}
