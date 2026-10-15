package gates

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// parseJSCPD parses the jscpd JSON report (`jscpd --reporters json --output <dir>`,
// read via report_file: <dir>/jscpd-report.json).
func parseJSCPD(in ParseInput) (ParseOutput, error) {
	type loc struct {
		Line   int `json:"line"`
		Column int `json:"column"`
	}
	type file struct {
		Name     string `json:"name"`
		Start    int    `json:"start"`
		End      int    `json:"end"`
		StartLoc *loc   `json:"startLoc"`
	}
	var rep struct {
		Statistics *struct {
			Total struct {
				Lines           int     `json:"lines"`
				DuplicatedLines int     `json:"duplicatedLines"`
				Percentage      float64 `json:"percentage"`
				Clones          int     `json:"clones"`
			} `json:"total"`
		} `json:"statistics"`
		Duplicates []struct {
			Format     string `json:"format"`
			Lines      int    `json:"lines"`
			FirstFile  file   `json:"firstFile"`
			SecondFile file   `json:"secondFile"`
		} `json:"duplicates"`
	}
	if err := json.Unmarshal(extractJSON(in.Output, '{'), &rep); err != nil {
		return ParseOutput{}, fmt.Errorf("jscpd: %w: %v", ErrUnparseable, err)
	}
	if rep.Statistics == nil {
		return ParseOutput{}, fmt.Errorf("jscpd: %w: missing statistics", ErrUnparseable)
	}
	var out ParseOutput
	for _, d := range rep.Duplicates {
		f := Finding{File: d.FirstFile.Name, Line: d.FirstFile.Start, Rule: "duplication", Severity: SeverityWarning,
			Message: fmt.Sprintf("%d duplicated lines (%s) also in %s:%d-%d",
				d.Lines, d.Format, relPath(in, d.SecondFile.Name), d.SecondFile.Start, d.SecondFile.End),
			Suggestion: "extract the shared code into one function/module and reuse it"}
		if d.FirstFile.StartLoc != nil {
			f.Line, f.Column = d.FirstFile.StartLoc.Line, d.FirstFile.StartLoc.Column
		}
		out.Findings = append(out.Findings, f)
	}
	pct := rep.Statistics.Total.Percentage
	out.DuplicationPct = &pct
	out.Summary = fmt.Sprintf("%.2f%% duplicated (%d of %d lines, %s)", pct,
		rep.Statistics.Total.DuplicatedLines, rep.Statistics.Total.Lines, plural(rep.Statistics.Total.Clones, "clone"))
	return out, nil
}

// parseKnip parses `knip --reporter json`. The issue categories vary across
// knip versions, so every array/object category on an issue is handled
// generically: arrays of {name,line,col}, maps of such arrays (enumMembers,
// classMembers), and arrays of arrays (duplicates).
func parseKnip(in ParseInput) (ParseOutput, error) {
	var rep struct {
		Files  []string                     `json:"files"`
		Issues []map[string]json.RawMessage `json:"issues"`
	}
	if err := json.Unmarshal(extractJSON(in.Output, '{'), &rep); err != nil {
		return ParseOutput{}, fmt.Errorf("knip: %w: %v", ErrUnparseable, err)
	}
	if rep.Files == nil && rep.Issues == nil {
		return ParseOutput{}, fmt.Errorf("knip: %w: not a knip json report", ErrUnparseable)
	}
	type item struct {
		Name string `json:"name"`
		Line int    `json:"line"`
		Col  int    `json:"col"`
	}
	var out ParseOutput
	for _, f := range rep.Files {
		out.Findings = append(out.Findings, Finding{File: f, Rule: "files", Severity: SeverityError, Message: "unused file", Suggestion: "delete it if nothing should import it"})
	}
	unusedFiles := map[string]bool{}
	for _, f := range rep.Files {
		unusedFiles[f] = true
	}
	add := func(file, cat, prefix string, it item) {
		if cat == "files" {
			// knip >= 5 reports unused files as an issue category.
			if !unusedFiles[it.Name] {
				unusedFiles[it.Name] = true
				out.Findings = append(out.Findings, Finding{File: it.Name, Rule: "files", Severity: SeverityError, Message: "unused file", Suggestion: "delete it if nothing should import it"})
			}
			return
		}
		out.Findings = append(out.Findings, Finding{File: file, Line: it.Line, Column: it.Col, Rule: cat, Severity: SeverityError,
			Message: fmt.Sprintf("%s: %s%s", knipLabel(cat), prefix, it.Name)})
	}
	for _, issue := range rep.Issues {
		var file string
		_ = json.Unmarshal(issue["file"], &file)
		cats := make([]string, 0, len(issue))
		for c := range issue {
			if c != "file" && c != "owners" {
				cats = append(cats, c)
			}
		}
		sort.Strings(cats)
		for _, cat := range cats {
			raw := issue[cat]
			var items []item
			if json.Unmarshal(raw, &items) == nil {
				for _, it := range items {
					if it.Name != "" {
						add(file, cat, "", it)
					}
				}
				continue
			}
			var groups [][]item // duplicates
			if json.Unmarshal(raw, &groups) == nil {
				for _, g := range groups {
					names := make([]string, 0, len(g))
					for _, it := range g {
						names = append(names, it.Name)
					}
					if len(g) > 0 {
						add(file, cat, "", item{Name: strings.Join(names, " = "), Line: g[0].Line, Col: g[0].Col})
					}
				}
				continue
			}
			var byParent map[string][]item // enumMembers / classMembers
			if json.Unmarshal(raw, &byParent) == nil {
				parents := make([]string, 0, len(byParent))
				for p := range byParent {
					parents = append(parents, p)
				}
				sort.Strings(parents)
				for _, p := range parents {
					for _, it := range byParent[p] {
						add(file, cat, p+".", it)
					}
				}
			}
		}
	}
	out.Summary = plural(len(out.Findings), "unused item")
	return out, nil
}

func knipLabel(cat string) string {
	switch cat {
	case "dependencies":
		return "unused dependency"
	case "devDependencies":
		return "unused devDependency"
	case "optionalPeerDependencies":
		return "unused optional peer dependency"
	case "unlisted":
		return "unlisted dependency"
	case "binaries":
		return "unlisted binary"
	case "unresolved":
		return "unresolved import"
	case "exports", "nsExports":
		return "unused export"
	case "types", "nsTypes":
		return "unused exported type"
	case "enumMembers":
		return "unused enum member"
	case "classMembers":
		return "unused class member"
	case "duplicates":
		return "duplicate export"
	}
	return cat
}
