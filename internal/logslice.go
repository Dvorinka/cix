package internal

import (
	"regexp"
	"strings"
)

// Log slicing turns a multi-thousand-line Actions log into the smallest
// actionable window: the failing step's error block plus context.

var errorPatterns = []*regexp.Regexp{
	regexp.MustCompile(`FAILURE:`),
	regexp.MustCompile(`\bFAILED\b`),
	regexp.MustCompile(`\* What went wrong`),
	regexp.MustCompile(`^e: file://`),
	regexp.MustCompile(`error TS\d+`),
	regexp.MustCompile(`(?i)\berror:`),
	regexp.MustCompile(`(?i)exit code|exit value \d+|non-zero exit`),
	regexp.MustCompile(`panic:`),
	regexp.MustCompile(`##\[error\]`),
	regexp.MustCompile(`(?i)cannot find module`),
	regexp.MustCompile(`--- FAIL:`),
	regexp.MustCompile(`(?i)assertion|expected.*got`),
}

const maxSliceLines = 30

// SliceLog extracts the actionable failure window from a job log.
// failedStep scopes the search to that step's block when the log has
// recognizable step boundaries; otherwise the whole log is scanned.
// If no error pattern matches, the last ~40 lines are returned.
func SliceLog(log, failedStep string) string {
	lines := strings.Split(log, "\n")

	// Scope to the failed step's block if we can find its boundaries.
	block := lines
	if failedStep != "" {
		if b := stepBlock(lines, failedStep); len(b) > 0 {
			block = b
		}
	}

	// First error match in the block, with context.
	for i, l := range block {
		if matchesError(l) {
			start := i - 5
			if start < 0 {
				start = 0
			}
			end := i + 10
			if end > len(block) {
				end = len(block)
			}
			out := dedupeAdjacent(block[start:end])
			// chase continuation lines (> indented detail, stack frames)
			for end < len(block) && len(out) < maxSliceLines &&
				(strings.HasPrefix(block[end], ">") || strings.HasPrefix(block[end], "\t") ||
					strings.HasPrefix(block[end], "    ") || matchesError(block[end]) ||
					strings.Contains(block[end], "at ") || strings.Contains(block[end], "Caused by")) {
				out = append(out, block[end])
				end++
			}
			if len(out) > maxSliceLines {
				out = out[:maxSliceLines]
			}
			return strings.Join(out, "\n")
		}
	}

	// Fallback: last ~40 lines of the whole log.
	tail := lines
	if len(tail) > 40 {
		tail = tail[len(tail)-40:]
	}
	return strings.Join(dedupeAdjacent(tail), "\n")
}

// stepBlock extracts the lines belonging to a named step. GH Actions job
// logs delimit steps via "##[group]Run <step>" / "##[endgroup]" markers or
// per-step sections; we match the step name loosely.
func stepBlock(lines []string, step string) []string {
	start, end := -1, len(lines)
	for i, l := range lines {
		if strings.Contains(l, "##[group]") && strings.Contains(l, step) {
			start = i
		}
		if start >= 0 && i > start && strings.Contains(l, "##[group]") {
			end = i
			break
		}
	}
	if start < 0 {
		// Newer logs: step headers like "##[step:name]" or bare section split
		for i, l := range lines {
			if strings.Contains(l, step) && (strings.Contains(l, "##[") ||
				strings.Contains(strings.ToLower(l), "run ")) {
				start = i
			}
		}
		if start < 0 {
			return nil
		}
	}
	return lines[start:end]
}

func matchesError(l string) bool {
	for _, re := range errorPatterns {
		if re.MatchString(l) {
			return true
		}
	}
	return false
}

func dedupeAdjacent(lines []string) []string {
	var out []string
	prev := ""
	for _, l := range lines {
		s := stripTS(l)
		if s == prev {
			continue
		}
		prev = s
		out = append(out, l)
	}
	return out
}

func stripTS(l string) string {
	if i := strings.IndexByte(l, ' '); i > 0 && i < 40 && tsRe.MatchString(l[:i]) {
		return l[i+1:]
	}
	return l
}
