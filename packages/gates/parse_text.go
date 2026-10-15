package gates

import (
	"bufio"
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// 23 mypkg (*Server).handle internal/server/server.go:41:1
var gocycloRE = regexp.MustCompile(`^(\d+) (\S+) (\S+) (.+):(\d+):(\d+)$`)

// parseGocyclo parses gocyclo text output (`gocyclo -over N .`). Functions
// with complexity <= MaxComplexity (when > 0) are dropped.
func parseGocyclo(in ParseInput) (ParseOutput, error) {
	var out ParseOutput
	sc := bufio.NewScanner(bytes.NewReader(in.Output))
	unknown := 0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "Average:") {
			continue
		}
		m := gocycloRE.FindStringSubmatch(line)
		if m == nil {
			unknown++
			continue
		}
		cx, _ := strconv.Atoi(m[1])
		if in.MaxComplexity > 0 && cx <= in.MaxComplexity {
			continue
		}
		ln, _ := strconv.Atoi(m[5])
		col, _ := strconv.Atoi(m[6])
		msg := fmt.Sprintf("function %s.%s has cyclomatic complexity %d", m[2], m[3], cx)
		if in.MaxComplexity > 0 {
			msg += fmt.Sprintf(" (max %d)", in.MaxComplexity)
		}
		out.Findings = append(out.Findings, Finding{
			File: m[4], Line: ln, Column: col, Rule: "gocyclo", Severity: SeverityError,
			Message: msg, Suggestion: "split the function into smaller functions",
		})
	}
	if len(out.Findings) == 0 && unknown > 0 {
		return out, fmt.Errorf("gocyclo: %w: %d unrecognized lines", ErrUnparseable, unknown)
	}
	out.Summary = plural(len(out.Findings), "function") + " over the complexity limit"
	return out, nil
}

// internal/x/x.go:12:6: unreachable func: helper
var deadcodeRE = regexp.MustCompile(`^(.+?):(\d+):(\d+): (.*)$`)

// parseDeadcode parses golang.org/x/tools/cmd/deadcode text output.
func parseDeadcode(in ParseInput) (ParseOutput, error) {
	var out ParseOutput
	sc := bufio.NewScanner(bytes.NewReader(in.Output))
	unknown := 0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		m := deadcodeRE.FindStringSubmatch(line)
		if m == nil {
			unknown++
			continue
		}
		ln, _ := strconv.Atoi(m[2])
		col, _ := strconv.Atoi(m[3])
		rule := "deadcode"
		if i := strings.Index(m[4], ":"); i > 0 {
			rule = strings.ReplaceAll(strings.TrimSpace(m[4][:i]), " ", "-") // "unreachable-func"
		}
		out.Findings = append(out.Findings, Finding{
			File: m[1], Line: ln, Column: col, Rule: rule, Severity: SeverityError, Message: m[4],
			Suggestion: "remove it, or use it if it should be reachable",
		})
	}
	if len(out.Findings) == 0 && unknown > 0 {
		return out, fmt.Errorf("deadcode: %w: %d unrecognized lines", ErrUnparseable, unknown)
	}
	out.Summary = plural(len(out.Findings), "unreachable declaration")
	return out, nil
}
