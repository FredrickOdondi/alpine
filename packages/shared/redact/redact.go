// Package redact scrubs secrets (API keys, tokens, private keys) from text before it is logged or
// sent to a model provider (gitleaks-style rules, architecture §9.2 "outbound prompt secret
// scanning").
package redact

import (
	"regexp"
	"strings"
)

// Rule is one secret pattern.
type Rule struct {
	ID      string
	Pattern *regexp.Regexp
}

// DefaultRules covers common credentials that show up in issues, logs and stack traces.
var DefaultRules = []Rule{
	{"private-key", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`)},
	{"openai-key", regexp.MustCompile(`\bsk-(?:proj-|svcacct-|admin-)?[A-Za-z0-9_\-]{20,}`)},
	{"anthropic-key", regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_\-]{20,}`)},
	{"github-token", regexp.MustCompile(`\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{30,}`)},
	{"github-pat", regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{40,}`)},
	{"aws-access-key", regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`)},
	{"slack-token", regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}`)},
	{"stripe-key", regexp.MustCompile(`\b(?:sk|rk)_(?:live|test)_[A-Za-z0-9]{16,}`)},
	{"jwt", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`)},
	{"bearer", regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9\-._~+/]{16,}=*`)},
	{"assignment", regexp.MustCompile(`(?i)\b(api[_-]?key|secret|password|passwd|token|access[_-]?key)\b(\s*[:=]\s*)["']?[^\s"']{8,}`)},
	{"url-credentials", regexp.MustCompile(`([a-z][a-z0-9+.-]*://)[^/\s:@]+:[^/\s@]+@`)},
}

// Redactor replaces matches with a marker.
type Redactor struct {
	rules []Rule
}

// New returns a redactor with the default rules plus any extra rules.
func New(extra ...Rule) *Redactor {
	return &Redactor{rules: append(append([]Rule{}, DefaultRules...), extra...)}
}

// Redact returns s with every secret replaced by "[REDACTED:<rule>]" and the ids of the rules that
// matched (empty when clean).
func (r *Redactor) Redact(s string) (string, []string) {
	var hits []string
	for _, rule := range r.rules {
		if !rule.Pattern.MatchString(s) {
			continue
		}
		hits = append(hits, rule.ID)
		switch rule.ID {
		case "assignment":
			s = rule.Pattern.ReplaceAllString(s, "${1}${2}[REDACTED:assignment]")
		case "url-credentials":
			s = rule.Pattern.ReplaceAllString(s, "${1}[REDACTED:url-credentials]@")
		default:
			s = rule.Pattern.ReplaceAllString(s, "[REDACTED:"+rule.ID+"]")
		}
	}
	return s, hits
}

// String is Redact without the rule ids.
func (r *Redactor) String(s string) string {
	out, _ := r.Redact(s)
	return out
}

// Contains reports whether s contains any secret.
func (r *Redactor) Contains(s string) bool {
	for _, rule := range r.rules {
		if rule.Pattern.MatchString(s) {
			return true
		}
	}
	return false
}

// Truncate shortens s to max runes for logging.
func Truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return strings.ToValidUTF8(s[:max], "") + "…[truncated]"
}
