package activities

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/alpine-dev/alpine/packages/agents"
)

// repoContext builds a bounded, read-only snapshot of the checkout for refinement: the file list
// and the source files most related to the ticket text (keyword hits in path or content).
func repoContext(ctx context.Context, ws agents.Workspace, title, body string, maxBytes int) string {
	files, err := ws.ListFiles(ctx, ".")
	if err != nil || len(files) == 0 {
		return ""
	}
	words := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(title+" "+body), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len(w) >= 4 {
			words[w] = true
		}
	}
	type cand struct {
		path    string
		content string
		score   int
	}
	var cands []cand
	for _, f := range files {
		if strings.HasPrefix(f, ".alpine/") || strings.HasPrefix(f, "…") {
			continue
		}
		c, err := ws.ReadFile(ctx, f)
		if err != nil || len(c) > 12_000 || strings.ContainsRune(c, 0) {
			continue
		}
		lc, lp := strings.ToLower(c), strings.ToLower(f)
		score := 0
		for w := range words {
			score += strings.Count(lc, w)
			if strings.Contains(lp, w) {
				score += 5
			}
		}
		cands = append(cands, cand{f, c, score})
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].score > cands[j].score })
	var b strings.Builder
	b.WriteString("Files:\n" + strings.Join(files, "\n") + "\n")
	for _, c := range cands {
		if c.score == 0 || b.Len()+len(c.content) > maxBytes {
			continue
		}
		fmt.Fprintf(&b, "\n--- %s ---\n%s\n", c.path, c.content)
	}
	return b.String()
}
