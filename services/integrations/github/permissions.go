package github

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Level is a GitHub App permission level.
type Level string

const (
	Read  Level = "read"
	Write Level = "write"
)

// Permissions is the permission subset requested for an installation token,
// keyed by GitHub permission name (contents, pull_requests, checks, issues,
// metadata, ...). Always request the smallest set an activity needs; see
// README.md for the App-level ceiling.
type Permissions map[string]Level

// Presets for the activities this package supports. Each call returns a new
// map, so callers may merge or modify freely.
func PermsPush() Permissions { return Permissions{"contents": Write, "metadata": Read} }

// PermsPullRequests covers opening/updating PRs, PR labels and PR comments.
func PermsPullRequests() Permissions { return Permissions{"pull_requests": Write, "metadata": Read} }

// PermsChecks covers creating/updating check runs.
func PermsChecks() Permissions { return Permissions{"checks": Write, "metadata": Read} }

// PermsIssueComments covers commenting on / labeling issues.
func PermsIssueComments() Permissions { return Permissions{"issues": Write, "metadata": Read} }

// Merge returns the union of permission sets; write wins over read.
func Merge(sets ...Permissions) Permissions {
	out := Permissions{}
	for _, s := range sets {
		for k, v := range s {
			if out[k] != Write {
				out[k] = v
			}
		}
	}
	return out
}

// key is a canonical string form, used for token cache keys.
func (p Permissions) key() string {
	var b strings.Builder
	for _, k := range slices.Sorted(maps.Keys(p)) {
		fmt.Fprintf(&b, "%s=%s;", k, p[k])
	}
	return b.String()
}

func (p Permissions) validate() error {
	if len(p) == 0 {
		return fmt.Errorf("github: token request must name explicit permissions")
	}
	for k, v := range p {
		if k == "" || (v != Read && v != Write) {
			return fmt.Errorf("github: invalid permission %q=%q", k, v)
		}
		if k == "workflows" || k == "administration" || k == "secrets" {
			// Never needed by Alpine and denied at the App level (architecture §9.1).
			return fmt.Errorf("github: permission %q is not allowed", k)
		}
	}
	return nil
}
