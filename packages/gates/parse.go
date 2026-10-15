package gates

import (
	"bytes"
	"errors"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ParseInput is what a parser receives.
type ParseInput struct {
	// Output is the tool's report: stdout, or the content of report_file.
	Output []byte
	// RepoDir is the repo root; absolute paths under it are made relative.
	RepoDir string
	// WorkDir is the directory the command ran in (absolute).
	WorkDir string
	// MaxComplexity is passed to complexity parsers (0 = keep all).
	MaxComplexity int
}

// ParseOutput is the normalized parser result.
type ParseOutput struct {
	Findings []Finding
	// Summary is a short tool-specific summary, e.g. "3 errors, 1 warning in 2 files".
	Summary string
	// DuplicationPct is set by duplication parsers.
	DuplicationPct *float64
}

// Parser converts a tool's output into findings.
type Parser func(in ParseInput) (ParseOutput, error)

// ErrUnparseable is returned (wrapped) when output is not in the expected format.
var ErrUnparseable = errors.New("unparseable tool output")

var parsers = map[string]Parser{
	"eslint":        parseESLint,
	"golangci-lint": parseGolangCI,
	"tsc":           parseTSC,
	"go-test":       parseGoTest,
	"jest":          parseJest,
	"vitest":        parseVitest, // vitest's json reporter is jest-compatible
	"jscpd":         parseJSCPD,
	"gocyclo":       parseGocyclo,
	"knip":          parseKnip,
	"deadcode":      parseDeadcode,
}

// ParserNames returns the registered parser names, sorted.
func ParserNames() []string {
	names := make([]string, 0, len(parsers)+1)
	for n := range parsers {
		names = append(names, n)
	}
	names = append(names, "none")
	sort.Strings(names)
	return names
}

// Parse runs the named parser. "none" or "" returns no findings.
func Parse(name string, in ParseInput) (ParseOutput, error) {
	if name == "" || name == "none" {
		return ParseOutput{}, nil
	}
	p, ok := parsers[name]
	if !ok {
		return ParseOutput{}, errors.New("unknown parser " + name)
	}
	out, err := p(in)
	if err != nil {
		return out, err
	}
	for i := range out.Findings {
		f := &out.Findings[i]
		f.File = relPath(in, f.File)
		f.Message = strings.TrimSpace(stripANSI(f.Message))
		if f.Severity == "" {
			f.Severity = SeverityError
		}
	}
	SortFindings(out.Findings)
	return out, nil
}

// SortFindings orders findings deterministically: errors first, then by file,
// line, column, rule, message.
func SortFindings(fs []Finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		if ra, rb := severityRank(a.Severity), severityRank(b.Severity); ra != rb {
			return ra < rb
		}
		if a.File != b.File {
			if a.File == "" || b.File == "" {
				return a.File != "" // located findings first
			}
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Column != b.Column {
			return a.Column < b.Column
		}
		if a.Rule != b.Rule {
			return a.Rule < b.Rule
		}
		return a.Message < b.Message
	})
}

func severityRank(s Severity) int {
	switch s {
	case SeverityError:
		return 0
	case SeverityWarning:
		return 1
	default:
		return 2
	}
}

func relPath(in ParseInput, p string) string {
	if p == "" {
		return ""
	}
	p = filepath.ToSlash(p)
	if !filepath.IsAbs(p) && in.WorkDir != "" && in.RepoDir != "" && in.WorkDir != in.RepoDir {
		// Relative to the command's working dir: make it repo-relative.
		if r, err := filepath.Rel(in.RepoDir, filepath.Join(in.WorkDir, p)); err == nil && !strings.HasPrefix(r, "..") {
			return filepath.ToSlash(r)
		}
	}
	if filepath.IsAbs(p) && in.RepoDir != "" {
		if r, err := filepath.Rel(in.RepoDir, p); err == nil && !strings.HasPrefix(r, "..") {
			return filepath.ToSlash(r)
		}
	}
	return strings.TrimPrefix(p, "./")
}

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

func stripANSI(s string) string { return ansiRE.ReplaceAllString(s, "") }

// extractJSON trims leading/trailing noise (e.g. package-manager banners)
// around a JSON document that starts with open ('[' or '{').
func extractJSON(b []byte, open byte) []byte {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == open {
		return b
	}
	closer := byte('}')
	if open == '[' {
		closer = ']'
	}
	start := bytes.IndexByte(b, open)
	end := bytes.LastIndexByte(b, closer)
	if start < 0 || end < start {
		return b
	}
	return b[start : end+1]
}

func plural(n int, word string) string {
	s := strconv.Itoa(n) + " " + word
	if n != 1 {
		s += "s"
	}
	return s
}

func countSeverities(fs []Finding) (errs, warns int) {
	for _, f := range fs {
		switch f.Severity {
		case SeverityError, "":
			errs++
		case SeverityWarning:
			warns++
		}
	}
	return
}

func severitySummary(fs []Finding) string {
	e, w := countSeverities(fs)
	files := map[string]bool{}
	for _, f := range fs {
		if f.File != "" {
			files[f.File] = true
		}
	}
	s := plural(e, "error") + ", " + plural(w, "warning")
	if len(files) > 0 {
		s += " in " + plural(len(files), "file")
	}
	return s
}
