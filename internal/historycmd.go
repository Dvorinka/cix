package internal

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
)

// HistoryOpts controls `cix history`.
type HistoryOpts struct {
	Job      string
	Failures bool
	JSON     bool
	Path     string
}

// History prints local CI history: per-job aggregates or raw failures.
func History(opts HistoryOpts, out io.Writer) int {
	path := opts.Path
	if path == "" {
		path = HistoryPath()
	}
	recs := ReadHistoryFrom(path)
	if len(recs) == 0 {
		fmt.Fprintln(out, "no history yet — cix records every run it observes")
		return 0
	}

	if opts.JSON {
		var data any = HistoryStats(recs)
		if opts.Failures {
			data = filterFailures(recs, opts.Job)
		}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		_ = enc.Encode(data)
		return 0
	}

	if opts.Failures {
		fails := filterFailures(recs, opts.Job)
		for _, r := range fails {
			fmt.Fprintf(out, "run %d  %s  %s — %s (%s)  sig=%s\n",
				r.RunID, r.Workflow, r.Job, r.Conclusion, r.TS, orDash(r.Signature, ""))
		}
		if len(fails) == 0 {
			fmt.Fprintln(out, "no failures recorded")
		}
		return 0
	}

	stats := HistoryStats(recs)
	for _, s := range stats {
		if opts.Job != "" && s.Job != opts.Job && BaseJobName(s.Job) != opts.Job {
			continue
		}
		fmt.Fprintf(out, "%-32s %3d runs  p50 %-7s p90 %-7s last: %s (run %d, %s)\n",
			s.Workflow+"/"+s.Job, s.Count, dur(s.P50Seconds), dur(s.P90Seconds),
			orDash(s.LastConclusion, ""), s.LastRunID, s.LastTS)
	}
	return 0
}

func filterFailures(recs []HistoryRecord, job string) []HistoryRecord {
	var out []HistoryRecord
	for _, r := range recs {
		if !failureConclusions[r.Conclusion] {
			continue
		}
		if job != "" && r.Job != job && BaseJobName(r.Job) != job {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TS < out[j].TS })
	return out
}
