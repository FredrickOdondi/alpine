package gates

import (
	"encoding/json"
	"fmt"
	"strings"
)

// parseESLint parses `eslint -f json` output.
func parseESLint(in ParseInput) (ParseOutput, error) {
	var files []struct {
		FilePath string `json:"filePath"`
		Messages []struct {
			RuleID   *string `json:"ruleId"`
			Severity int     `json:"severity"` // 1 warn, 2 error
			Message  string  `json:"message"`
			Line     int     `json:"line"`
			Column   int     `json:"column"`
			Fatal    bool    `json:"fatal"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(extractJSON(in.Output, '['), &files); err != nil {
		return ParseOutput{}, fmt.Errorf("eslint: %w: %v", ErrUnparseable, err)
	}
	var out ParseOutput
	for _, f := range files {
		for _, m := range f.Messages {
			rule := ""
			if m.RuleID != nil {
				rule = *m.RuleID
			}
			if m.Fatal && rule == "" {
				rule = "parse-error"
			}
			sev := SeverityWarning
			if m.Severity >= 2 || m.Fatal {
				sev = SeverityError
			}
			out.Findings = append(out.Findings, Finding{
				File: f.FilePath, Line: m.Line, Column: m.Column,
				Rule: rule, Severity: sev, Message: m.Message,
			})
		}
	}
	out.Summary = severitySummary(out.Findings)
	return out, nil
}

// parseGolangCI parses golangci-lint JSON output (v1 `--out-format json`,
// v2 `--output.json.path stdout`; both emit {"Issues": [...]}).
func parseGolangCI(in ParseInput) (ParseOutput, error) {
	var rep struct {
		Issues *[]struct {
			FromLinter  string   `json:"FromLinter"`
			Text        string   `json:"Text"`
			Severity    string   `json:"Severity"`
			SourceLines []string `json:"SourceLines"`
			Pos         struct {
				Filename string `json:"Filename"`
				Line     int    `json:"Line"`
				Column   int    `json:"Column"`
			} `json:"Pos"`
		} `json:"Issues"`
	}
	if err := json.Unmarshal(extractJSON(in.Output, '{'), &rep); err != nil {
		return ParseOutput{}, fmt.Errorf("golangci-lint: %w: %v", ErrUnparseable, err)
	}
	var out ParseOutput
	if rep.Issues != nil {
		for _, is := range *rep.Issues {
			sev := SeverityError
			switch strings.ToLower(is.Severity) {
			case "warning", "warn":
				sev = SeverityWarning
			case "info", "hint":
				sev = SeverityInfo
			}
			out.Findings = append(out.Findings, Finding{
				File: is.Pos.Filename, Line: is.Pos.Line, Column: is.Pos.Column,
				Rule: is.FromLinter, Severity: sev, Message: is.Text,
			})
		}
	}
	out.Summary = severitySummary(out.Findings)
	return out, nil
}
