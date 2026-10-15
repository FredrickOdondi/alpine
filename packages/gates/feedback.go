package gates

import (
	"fmt"
	"strings"
)

// Feedback limits.
const (
	MaxFeedbackGroupsPerGate = 10   // distinct (rule, message) groups shown per gate
	MaxGateFeedbackChars     = 1800 // per gate
	MaxRunFeedbackChars      = 6000 // whole run
	maxFeedbackMessageChars  = 300  // per finding message
	maxExtraLocations        = 3    // extra locations listed for a repeated finding
)

// GateFeedback renders a compact, deduped, capped text for the agent. Findings
// are already sorted most-actionable first (errors with a location, then
// errors without, then warnings); identical (severity, rule, message) findings
// are collapsed into one line with their locations.
func GateFeedback(r GateResult) string {
	label := r.GateID
	if r.Parser != "" && r.Parser != "none" {
		label += " (" + r.Parser + ")"
	}
	head := fmt.Sprintf("%s: %s", label, strings.ToUpper(string(r.Status)))
	if r.Summary != "" {
		head += " - " + r.Summary
	}
	if r.Status == StatusPass || r.Status == StatusSkipped || len(r.Findings) == 0 {
		return head
	}

	type group struct {
		first Finding
		locs  []string
	}
	var groups []*group
	index := map[string]*group{}
	for _, f := range r.Findings {
		msg := truncate(oneLine(f.Message), maxFeedbackMessageChars)
		key := string(f.Severity) + "\x00" + f.Rule + "\x00" + msg
		if g, ok := index[key]; ok {
			if l := location(f); l != "" && !contains(g.locs, l) && l != location(g.first) {
				g.locs = append(g.locs, l)
			}
			continue
		}
		g := &group{first: f}
		index[key] = g
		groups = append(groups, g)
	}

	var b strings.Builder
	b.WriteString(head)
	shown := 0
	for _, g := range groups {
		if shown == MaxFeedbackGroupsPerGate {
			break
		}
		line := "\n- " + findingLine(g.first)
		if n := len(g.locs); n > 0 {
			extra := g.locs
			if n > maxExtraLocations {
				extra = extra[:maxExtraLocations]
			}
			line += " (also at " + strings.Join(extra, ", ")
			if n > maxExtraLocations {
				line += fmt.Sprintf(" and %d more", n-maxExtraLocations)
			}
			line += ")"
		}
		if b.Len()+len(line) > MaxGateFeedbackChars-40 {
			break
		}
		b.WriteString(line)
		shown++
	}
	if rest := len(groups) - shown; rest > 0 {
		fmt.Fprintf(&b, "\n- ... and %d more distinct findings", rest)
	}
	return b.String()
}

// RunFeedback renders feedback for a whole run: failing gates first, then
// errors and warnings, then a one-line list of passing/skipped gates.
func RunFeedback(r RunResult) string {
	if r.CapReached {
		return fmt.Sprintf("Gate-run cap reached (%d/%d): no more gate runs for this job. Stop and escalate to a human with a diagnosis.", r.GateRunsUsed, r.GateRunCap)
	}
	var b strings.Builder
	failing := 0
	for _, g := range r.Gates {
		if g.Status == StatusFail || g.Status == StatusError {
			failing++
		}
	}
	fmt.Fprintf(&b, "Gate run %d/%d: %s", r.GateRunsUsed, r.GateRunCap, strings.ToUpper(string(r.Status)))
	if failing > 0 {
		fmt.Fprintf(&b, " (%d of %d gates not passing)", failing, len(r.Gates))
	}
	var ok []string
	for _, st := range []Status{StatusFail, StatusError, StatusWarn} {
		for _, g := range r.Gates {
			if g.Status != st {
				continue
			}
			fb := g.Feedback
			if fb == "" {
				fb = GateFeedback(g)
			}
			if b.Len()+len(fb)+2 > MaxRunFeedbackChars-200 {
				fb = truncate(fb, max(0, MaxRunFeedbackChars-200-b.Len()-2))
				if fb == "" {
					continue
				}
			}
			b.WriteString("\n\n" + fb)
		}
	}
	for _, g := range r.Gates {
		if g.Status == StatusPass || g.Status == StatusSkipped {
			ok = append(ok, g.GateID+" "+string(g.Status))
		}
	}
	if len(ok) > 0 {
		b.WriteString("\n\nOK: " + strings.Join(ok, ", "))
	}
	if r.Status != StatusPass && r.GateRunCap > 0 {
		left := r.GateRunCap - r.GateRunsUsed
		fmt.Fprintf(&b, "\n\n%d gate runs left. Fix the errors above before asking for another run.", left)
	}
	return truncate(b.String(), MaxRunFeedbackChars)
}

func findingLine(f Finding) string {
	s := ""
	if l := location(f); l != "" {
		s = l + " "
	}
	if f.Rule != "" {
		s += "[" + f.Rule + "] "
	}
	if f.Severity == SeverityWarning {
		s += "(warning) "
	}
	s += truncate(oneLine(f.Message), maxFeedbackMessageChars)
	if f.Suggestion != "" {
		s += " -> " + f.Suggestion
	}
	return s
}

func location(f Finding) string {
	switch {
	case f.File == "":
		return ""
	case f.Line > 0 && f.Column > 0:
		return fmt.Sprintf("%s:%d:%d", f.File, f.Line, f.Column)
	case f.Line > 0:
		return fmt.Sprintf("%s:%d", f.File, f.Line)
	}
	return f.File
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, "\n", " | ")), " ")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 3 {
		return ""
	}
	// Avoid splitting a UTF-8 sequence.
	cut := n - 3
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "..."
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
