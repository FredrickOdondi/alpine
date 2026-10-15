package gates

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// ratchetVersion is part of the baseline cache key; bump it when parsing or
// matching changes in a way that invalidates cached baselines.
const ratchetVersion = 1

// Baseline is a ratchet gate's parsed result on the base SHA.
type Baseline struct {
	BaseSHA        string    `json:"baseSha"`
	GateID         string    `json:"gateId"`
	Findings       []Finding `json:"findings"`
	DuplicationPct *float64  `json:"duplicationPct,omitempty"`
}

// BaselineCache stores baselines keyed by base SHA + gate config hash (see
// baselineKey). Implementations must be safe for concurrent use.
type BaselineCache interface {
	Get(ctx context.Context, key string) (*Baseline, bool, error)
	Put(ctx context.Context, key string, b *Baseline) error
}

// MemoryBaselineCache is an in-process BaselineCache.
type MemoryBaselineCache struct {
	mu sync.Mutex
	m  map[string]Baseline
}

// NewMemoryBaselineCache returns an empty MemoryBaselineCache.
func NewMemoryBaselineCache() *MemoryBaselineCache {
	return &MemoryBaselineCache{m: map[string]Baseline{}}
}

// Get implements BaselineCache.
func (c *MemoryBaselineCache) Get(_ context.Context, key string) (*Baseline, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.m[key]
	if !ok {
		return nil, false, nil
	}
	cp := b
	cp.Findings = append([]Finding(nil), b.Findings...)
	return &cp, true, nil
}

// Put implements BaselineCache.
func (c *MemoryBaselineCache) Put(_ context.Context, key string, b *Baseline) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := *b
	cp.Findings = append([]Finding(nil), b.Findings...)
	c.m[key] = cp
	return nil
}

// DirBaselineCache stores baselines as JSON files in Dir (one file per key).
type DirBaselineCache struct{ Dir string }

var cacheKeyRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (c DirBaselineCache) path(key string) (string, error) {
	if !cacheKeyRE.MatchString(key) {
		return "", fmt.Errorf("gates: invalid baseline cache key %q", key)
	}
	return filepath.Join(c.Dir, key+".json"), nil
}

// Get implements BaselineCache.
func (c DirBaselineCache) Get(_ context.Context, key string) (*Baseline, bool, error) {
	p, err := c.path(key)
	if err != nil {
		return nil, false, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var bl Baseline
	if err := json.Unmarshal(b, &bl); err != nil {
		return nil, false, nil // corrupt entry: treat as a miss, it will be rewritten
	}
	return &bl, true, nil
}

// Put implements BaselineCache (atomic write via rename).
func (c DirBaselineCache) Put(_ context.Context, key string, bl *Baseline) error {
	p, err := c.path(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(c.Dir, 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(bl)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(c.Dir, ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), p)
}

// baselineKey hashes everything that can change a gate's base result: the
// base commit, the full gate config, the env allowlist, base_setup and the
// ratchet/parser version.
func baselineKey(baseSHA string, cfg Config, g GateConfig) string {
	payload, _ := json.Marshal(struct {
		V         int
		BaseSHA   string
		Gate      GateConfig
		Allowlist []string
		BaseSetup string
	}{ratchetVersion, baseSHA, g, cfg.Runner.EnvAllowlist, cfg.Runner.BaseSetup})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// baseline returns the gate's result on the base SHA, from cache or by running
// the gate in the temporary base checkout.
func (rs *runState) baseline(ctx context.Context, g GateConfig) (*Baseline, bool, error) {
	sha, err := rs.baseSHA(ctx)
	if err != nil {
		return nil, false, err
	}
	key := baselineKey(sha, rs.cfg, g)
	if c := rs.r.Baselines; c != nil {
		if b, ok, err := c.Get(ctx, key); err == nil && ok {
			return b, true, nil
		}
	}
	dir, err := rs.baseDir(ctx)
	if err != nil {
		return nil, false, err
	}
	ex := rs.r.execute(ctx, rs.cfg, g, dir, rs.baseRef, "base")
	switch {
	case ex.setupErr != nil:
		return nil, false, ex.setupErr
	case ex.timedOut:
		return nil, false, fmt.Errorf("gate timed out on the base checkout (limit %s)", ex.timeout)
	case ex.startErr != nil:
		return nil, false, ex.startErr
	case ex.parseErr != nil:
		return nil, false, fmt.Errorf("%v\n%s", ex.parseErr, outputTail(ex.stderr, ex.stdout))
	case crashedWithoutFindings(ex):
		return nil, false, fmt.Errorf("gate exited %d on the base checkout without reporting findings:\n%s", ex.exitCode, outputTail(ex.stderr, ex.stdout))
	}
	b := &Baseline{BaseSHA: sha, GateID: g.ID, Findings: filterRules(ex.parsed.Findings, g.OnlyRules), DuplicationPct: ex.parsed.DuplicationPct}
	if c := rs.r.Baselines; c != nil {
		_ = c.Put(ctx, key, b) // a cache write failure only costs a recomputation
	}
	return b, false, nil
}

// ratchet evaluates a gate against its base-SHA baseline: only regressions
// introduced by the diff fail; pre-existing findings are reported as info.
func (rs *runState) ratchet(ctx context.Context, gr GateResult, g GateConfig, ex execResult) GateResult {
	if crashedWithoutFindings(ex) {
		// In ratchet mode the exit code is otherwise ignored, so a tool that
		// crashed (bad config, missing deps) must not read as "no findings".
		gr.Status = StatusFail
		gr.Summary = fmt.Sprintf("exit code %d without any parsed findings", ex.exitCode)
		gr.Findings = append(gr.Findings, Finding{Rule: "exit-code", Severity: SeverityError,
			Message: fmt.Sprintf("command exited with code %d but reported no findings:\n%s", ex.exitCode, outputTail(ex.stderr, ex.stdout))})
		return applyWarnOnly(gr, g)
	}
	head := filterRules(ex.parsed.Findings, g.OnlyRules)
	base, cached, err := rs.baseline(ctx, g)
	if err != nil {
		gr.Status = StatusError
		gr.Summary = "ratchet baseline unavailable for " + rs.baseRef
		gr.Findings = append(gr.Findings, Finding{Rule: "baseline", Severity: SeverityError,
			Message: "could not compute the base-branch baseline: " + err.Error()})
		for _, f := range head {
			f.Severity = SeverityInfo
			gr.Findings = append(gr.Findings, f)
		}
		SortFindings(gr.Findings)
		return gr
	}
	cmp := compareFindings(base.Findings, head)
	gr.Findings = append(gr.Findings, cmp.findings...)
	failed := cmp.regressions > 0
	var parts []string

	if g.Type == TypeDuplication && ex.parsed.DuplicationPct != nil && base.DuplicationPct != nil {
		// Duplication fails only on a rise in percentage beyond max_new_duplication_pct;
		// new clones within that budget are warnings.
		failed = false
		for i := range gr.Findings {
			if gr.Findings[i].Severity == SeverityError {
				gr.Findings[i].Severity = SeverityWarning
			}
		}
		allowed := 0.0
		if g.MaxNewDuplicationPct != nil {
			allowed = *g.MaxNewDuplicationPct
		}
		hp, bp := *ex.parsed.DuplicationPct, *base.DuplicationPct
		delta := hp - bp
		parts = append(parts, fmt.Sprintf("duplication %.2f%% (base %.2f%%, %+.2f)", hp, bp, delta))
		if delta > allowed+1e-9 {
			failed = true
			gr.Findings = append(gr.Findings, Finding{Rule: "duplication-delta", Severity: SeverityError,
				Message:    fmt.Sprintf("duplication rose from %.2f%% to %.2f%% (+%.2f points, max_new_duplication_pct %.2f)", bp, hp, delta, allowed),
				Suggestion: "reuse the existing code instead of copying it"})
		}
	}
	parts = append(parts, fmt.Sprintf("%s, %d pre-existing, %d fixed vs base", plural(cmp.regressions, "new issue"), cmp.preexisting, cmp.fixed))
	if cached {
		parts = append(parts, "baseline cached")
	}
	gr.Summary = strings.Join(parts, "; ")
	SortFindings(gr.Findings)
	if failed {
		gr.Status = StatusFail
	} else {
		gr.Status = StatusPass
	}
	return applyWarnOnly(gr, g)
}

type comparison struct {
	findings                        []Finding
	regressions, preexisting, fixed int
}

// compareFindings matches head findings to base findings by a stable key
// (file + symbol/rule + normalized message, never the line number), as a
// multiset: the n-th head finding with a key pairs with the n-th base finding
// with that key (both in source order). Unpaired head findings are new;
// paired findings with a metric (complexity) regress if the metric rose.
func compareFindings(base, head []Finding) comparison {
	type entry struct {
		f      Finding
		metric float64
		hasM   bool
	}
	group := func(fs []Finding) (map[string][]entry, []string) {
		sorted := append([]Finding(nil), fs...)
		SortFindings(sorted)
		m := map[string][]entry{}
		var order []string
		for _, f := range sorted {
			k, metric, hasM := findingKey(f)
			if _, ok := m[k]; !ok {
				order = append(order, k)
			}
			m[k] = append(m[k], entry{f, metric, hasM})
		}
		return m, order
	}
	bm, _ := group(base)
	hm, horder := group(head)
	var c comparison
	for _, k := range horder {
		bs := bm[k]
		for i, h := range hm[k] {
			f := h.f
			switch {
			case i >= len(bs):
				c.regressions++
				f.Severity = SeverityError
				f.Message = "new: " + f.Message
			case h.hasM && bs[i].hasM && h.metric > bs[i].metric:
				c.regressions++
				f.Severity = SeverityError
				f.Message = fmt.Sprintf("worse: %s (was %s on base)", f.Message, strconv.FormatFloat(bs[i].metric, 'f', -1, 64))
			default:
				c.preexisting++
				f.Severity = SeverityInfo
				f.Message = "pre-existing: " + f.Message
			}
			c.findings = append(c.findings, f)
		}
		if n := len(bs) - len(hm[k]); n > 0 {
			c.fixed += n
		}
	}
	for k, bs := range bm {
		if _, ok := hm[k]; !ok {
			c.fixed += len(bs)
		}
	}
	return c
}

var (
	// "function pkg.(*T).F has cyclomatic complexity 23 (max 15)" (gocyclo parser)
	cycloKeyRE = regexp.MustCompile(`^function (\S+) has cyclomatic complexity (\d+)`)
	// "Function 'handle' has a complexity of 14. Maximum allowed is 10." (ESLint complexity)
	eslintComplexityKeyRE = regexp.MustCompile(`^(.*?) has a complexity of (\d+)`)
	// "18 duplicated lines (typescript) also in src/b.ts:30-47" (jscpd parser)
	jscpdKeyRE = regexp.MustCompile(`^(\d+) duplicated lines \(([^)]*)\) also in (.+):\d+-\d+$`)
)

// findingKey returns a line-independent identity for a finding and, for
// complexity findings, the metric to compare.
func findingKey(f Finding) (key string, metric float64, hasMetric bool) {
	switch f.Rule {
	case "gocyclo":
		if m := cycloKeyRE.FindStringSubmatch(f.Message); m != nil {
			v, _ := strconv.ParseFloat(m[2], 64)
			return f.File + "\x00gocyclo\x00" + m[1], v, true
		}
	case "complexity":
		if m := eslintComplexityKeyRE.FindStringSubmatch(f.Message); m != nil {
			v, _ := strconv.ParseFloat(m[2], 64)
			return f.File + "\x00complexity\x00" + m[1], v, true
		}
	case "duplication":
		if m := jscpdKeyRE.FindStringSubmatch(f.Message); m != nil {
			return f.File + "\x00duplication\x00" + m[3] + "\x00" + m[2] + "\x00" + m[1], 0, false
		}
	}
	return f.File + "\x00" + f.Rule + "\x00" + normalizeMessage(f.Message), 0, false
}

var lineRefRE = regexp.MustCompile(`(?:\bline \d+|:\d+(?::\d+)?\b)`)

// normalizeMessage drops line references embedded in messages so a pure
// line shift does not make a finding look new.
func normalizeMessage(s string) string {
	return lineRefRE.ReplaceAllString(oneLine(s), "")
}

// crashedWithoutFindings reports a non-zero exit with nothing parsed: the
// tool failed for a reason other than the findings it reports.
func crashedWithoutFindings(ex execResult) bool {
	return ex.exitCode != 0 && len(ex.parsed.Findings) == 0 && ex.parsed.DuplicationPct == nil
}
