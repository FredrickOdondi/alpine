package gates

import (
	"bufio"
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	// src/a.ts(12,5): error TS2322: Type 'string' is not assignable to type 'number'.
	tscLocRE = regexp.MustCompile(`^(.+?)\((\d+),(\d+)\): (error|warning|message) (TS\d+): (.*)$`)
	// error TS5058: The specified path does not exist: 'tsconfig.json'.
	tscGlobalRE = regexp.MustCompile(`^(error|warning|message) (TS\d+): (.*)$`)
)

// parseTSC parses `tsc --noEmit --pretty false` plain-text output.
// Indented continuation lines are appended to the preceding diagnostic.
func parseTSC(in ParseInput) (ParseOutput, error) {
	var out ParseOutput
	sc := bufio.NewScanner(bytes.NewReader(in.Output))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		raw := stripANSI(strings.TrimRight(sc.Text(), "\r"))
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if m := tscLocRE.FindStringSubmatch(line); m != nil {
			ln, _ := strconv.Atoi(m[2])
			col, _ := strconv.Atoi(m[3])
			out.Findings = append(out.Findings, Finding{
				File: m[1], Line: ln, Column: col, Rule: m[5],
				Severity: tscSeverity(m[4]), Message: m[6],
			})
			continue
		}
		if m := tscGlobalRE.FindStringSubmatch(line); m != nil {
			out.Findings = append(out.Findings, Finding{Rule: m[2], Severity: tscSeverity(m[1]), Message: m[3]})
			continue
		}
		if len(out.Findings) > 0 && (strings.HasPrefix(raw, " ") || strings.HasPrefix(raw, "\t")) {
			last := &out.Findings[len(out.Findings)-1]
			if len(last.Message) < 1000 {
				last.Message += "\n" + line
			}
		}
		// Other lines ("Found 3 errors in 2 files.") are ignored.
	}
	if err := sc.Err(); err != nil {
		return out, fmt.Errorf("tsc: %w: %v", ErrUnparseable, err)
	}
	out.Summary = severitySummary(out.Findings)
	return out, nil
}

func tscSeverity(s string) Severity {
	switch s {
	case "warning":
		return SeverityWarning
	case "message":
		return SeverityInfo
	}
	return SeverityError
}
