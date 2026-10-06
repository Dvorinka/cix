package internal

import (
	"fmt"
	"regexp"
	"strings"
)

// Failure signatures let cix recognize a failure it has seen before.
// Normalization strips everything that varies between runs: paths,
// timestamps, SHAs, versions, line numbers, addresses.

var normRules = []struct {
	re  *regexp.Regexp
	rep string
}{
	{regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z?`), "<ts>"},
	{regexp.MustCompile(`[A-Za-z]:\\[^\s'"]+`), "<path>"},    // C:\...
	{regexp.MustCompile(`(?:/[\w.@+\-~]+){2,}/?`), "<path>"}, // /abs/paths
	{regexp.MustCompile(`\b[0-9a-f]{7,40}\b`), "<sha>"},      // hex shas
	{regexp.MustCompile(`0x[0-9a-fA-F]+`), "<addr>"},         // addresses
	{regexp.MustCompile(`\b\d+\.\d+(\.\d+)*\b`), "<ver>"},    // versions
	{regexp.MustCompile(`:\d+(?::\d+)?`), ":N"},              // line/col numbers
	{regexp.MustCompile(`\s+`), " "},
}

// NormalizeLine reduces an error line to a stable form for signature matching.
func NormalizeLine(s string) string {
	s = strings.TrimSpace(s)
	// strip leading GH Actions timestamp: "2025-01-15T10:30:00.1234567Z "
	if i := strings.IndexByte(s, ' '); i > 0 && tsRe.MatchString(s[:i]) {
		s = s[i+1:]
	}
	for _, r := range normRules {
		s = r.re.ReplaceAllString(s, r.rep)
	}
	return strings.TrimSpace(s)
}

var tsRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}`)

// classifier maps a normalized primary error line to a stable
// "<tool>/<kind>" signature. Unknown failures get "generic/<normalized>".
var classifiers = []struct {
	re   *regexp.Regexp
	name string
}{
	{regexp.MustCompile(`(?i)cannot find module`), "metro/module-not-found"},
	{regexp.MustCompile(`(?i)process 'command 'node'' finished with non-zero`), "metro/node-exit"},
	{regexp.MustCompile(`error TS\d+`), "tsc/type-error"},
	{regexp.MustCompile(`Execution failed for task '([^']+)'`), "gradle"},
	{regexp.MustCompile(`(?i)FAILURE:`), "gradle"},
	{regexp.MustCompile(`--- FAIL: (\S+)`), "gotest"},
	{regexp.MustCompile(`^e: file://`), "kotlin"},
	{regexp.MustCompile(`(?i)panic:`), "panic"},
	{regexp.MustCompile(`(?i)##\[error\]`), "gha"},
}

// SigRule is a user-configured signature override from .cix.yml — match a
// project-specific failure mode to a stable name.
type SigRule struct {
	Match string `yaml:"match"`
	Name  string `yaml:"name"`
	re    *regexp.Regexp
}

// CompileSigRules validates user signature rules; a bad regex is reported,
// not silently skipped.
func CompileSigRules(rules []SigRule) error {
	for i := range rules {
		re, err := regexp.Compile(rules[i].Match)
		if err != nil {
			return fmt.Errorf("signature rule %q: %w", rules[i].Name, err)
		}
		rules[i].re = re
	}
	return nil
}

// Signature derives a stable failure signature from a log slice:
// "<tool>/<kind>" for recognized classes, else a normalized line prefix.
func Signature(slice string) string {
	return signature(slice, nil)
}

// SignatureWith classifies with user rules first, then the built-ins.
func SignatureWith(slice string, rules []SigRule) string {
	return signature(slice, rules)
}

func signature(slice string, extra []SigRule) string {
	lines := strings.Split(slice, "\n")
	for _, r := range extra {
		if r.re != nil {
			for _, l := range lines {
				if r.re.MatchString(l) {
					return r.Name
				}
			}
		} else if re, err := regexp.Compile(r.Match); err == nil {
			for _, l := range lines {
				if re.MatchString(l) {
					return r.Name
				}
			}
		}
	}
	// prefer classifying the most specific lines first
	for _, c := range classifiers {
		for _, l := range lines {
			if m := c.re.FindStringSubmatch(l); m != nil {
				name := c.name
				if len(m) > 1 && m[1] != "" {
					name = name + "/" + strings.Trim(m[1], `'"`)
				}
				return name
			}
		}
	}
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		n := NormalizeLine(l)
		if len(n) > 200 {
			n = n[:200]
		}
		if len(n) < 8 {
			continue
		}
		return "generic/" + n
	}
	return "generic/empty"
}

var transientRe = regexp.MustCompile(`(?i)runner lost contact|runner has been marked offline|503 Service Unavailable|502 Bad Gateway|504 Gateway Timeout|ETIMEDOUT|ECONNRESET|Gradle daemon|OutOfMemoryError|Java heap space|The operation was canceled|cancelled|Internal Server Error|connection reset|network timeout`)

var deterministicRe = regexp.MustCompile(`error TS\d+|FAILURE:|--- FAIL:|panic:|compile|SyntaxError|TypeError`)

// IsTransient reports whether a log slice matches a known-transient failure
// signature (runner loss, 5xx, network timeouts, daemon OOM). Compile and
// test failures are never transient.
func IsTransient(slice string) bool {
	if deterministicRe.MatchString(slice) && !transientRe.MatchString(slice) {
		return false
	}
	return transientRe.MatchString(slice)
}
