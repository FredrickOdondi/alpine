package github

import (
	"errors"
	"fmt"
	"strings"
)

// Alpine branch names are BranchPrefix + <ticket ULID> + "-" + <slug>, e.g.
// alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout-total. The ULID makes the
// name unique per ticket; the slug is only for humans.
const (
	BranchPrefix = "alpine/"
	// MaxSlugLen caps the slug; a full name is at most 7+26+1+48 = 82 bytes.
	MaxSlugLen = 48
	ulidLen    = 26
	// maxRefLen is a conservative cap for any branch name the client writes.
	maxRefLen = 255
	// fallbackSlug is used when a title has no usable characters.
	fallbackSlug = "ticket"
)

// ErrInvalidBranchName is returned for branch names that are not valid git
// refs or, under BranchPrefix, do not follow alpine/<ULID>-<slug>.
var ErrInvalidBranchName = errors.New("github: invalid branch name")

// crockford is the ULID alphabet (Crockford base32, canonical uppercase).
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// ValidULID reports whether s is a canonical ULID: 26 uppercase Crockford
// base32 characters (no I, L, O, U) whose first character is 0-7, so the
// value fits in 128 bits. Lowercase is rejected so each ticket has exactly
// one branch name; NewBranchName canonicalizes lowercase input.
func ValidULID(s string) bool {
	if len(s) != ulidLen || s[0] > '7' {
		return false
	}
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(crockford, s[i]) < 0 {
			return false
		}
	}
	return true
}

// ValidSlug reports whether s is 1..MaxSlugLen lowercase ASCII letters and
// digits in hyphen-separated words (no leading, trailing or double hyphens).
func ValidSlug(s string) bool {
	if s == "" || len(s) > MaxSlugLen || s[0] == '-' || s[len(s)-1] == '-' || strings.Contains(s, "--") {
		return false
	}
	for i := 0; i < len(s); i++ {
		if b := s[i]; !(b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '-') {
			return false
		}
	}
	return true
}

// ParseBranchName splits an Alpine branch name into its ticket ULID and slug.
func ParseBranchName(name string) (ulid, slug string, err error) {
	rest, ok := strings.CutPrefix(name, BranchPrefix)
	if !ok || len(rest) < ulidLen+2 || rest[ulidLen] != '-' ||
		!ValidULID(rest[:ulidLen]) || !ValidSlug(rest[ulidLen+1:]) {
		return "", "", fmt.Errorf("%w: %q (want %s<ULID>-<slug>, uppercase ULID, slug [a-z0-9-]{1,%d})",
			ErrInvalidBranchName, name, BranchPrefix, MaxSlugLen)
	}
	return rest[:ulidLen], rest[ulidLen+1:], nil
}

// ValidateBranchName checks that name is a valid Alpine branch name.
func ValidateBranchName(name string) error {
	_, _, err := ParseBranchName(name)
	return err
}

// NewBranchName builds alpine/<ULID>-<slug> from a ticket ULID and title.
// The ULID may be given in lowercase and is canonicalized to uppercase; the
// slug is Slugify(title).
func NewBranchName(ticketULID, title string) (string, error) {
	u := strings.ToUpper(ticketULID)
	if !ValidULID(u) {
		return "", fmt.Errorf("%w: ticket ULID %q is not a valid ULID", ErrInvalidBranchName, ticketULID)
	}
	return BranchPrefix + u + "-" + Slugify(title), nil
}

// latinFold maps common accented Latin letters to ASCII so slugs stay
// readable ("Café crème" -> "cafe-creme").
var latinFold = func() map[rune]string {
	m := map[rune]string{'ß': "ss", 'æ': "ae", 'œ': "oe", 'ø': "o", 'đ': "d", 'ð': "d", 'þ': "th", 'ł': "l", 'ı': "i"}
	for to, from := range map[string]string{
		"a": "àáâãäåāăą", "c": "çćĉċč", "e": "èéêëēĕėęě", "g": "ĝğġģ", "h": "ĥħ", "i": "ìíîïĩīĭįİ",
		"j": "ĵ", "k": "ķ", "l": "ĺļľŀ", "n": "ñńņňŉ", "o": "òóôõöōŏő", "r": "ŕŗř", "s": "śŝşšș",
		"t": "ţťŧț", "u": "ùúûüũūŭůűų", "w": "ŵ", "y": "ýÿŷ", "z": "źżž",
	} {
		for _, r := range from {
			m[r] = to
		}
	}
	return m
}()

// Slugify turns a title into a branch slug: lowercase ASCII words joined by
// single hyphens, common Latin accents folded, everything else treated as a
// separator, cut to MaxSlugLen (at a word boundary when one is reasonably
// close). A title with nothing usable yields "ticket". The result always
// satisfies ValidSlug.
func Slugify(title string) string {
	var b strings.Builder
	sep := false
	for _, r := range strings.ToLower(title) {
		var s string
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			s = string(r)
		default:
			s = latinFold[r]
		}
		if s == "" {
			sep = b.Len() > 0
			continue
		}
		if sep {
			b.WriteByte('-')
			sep = false
		}
		b.WriteString(s)
	}
	out := b.String()
	if len(out) > MaxSlugLen {
		out = out[:MaxSlugLen]
		if i := strings.LastIndexByte(out, '-'); i >= MaxSlugLen/2 {
			out = out[:i]
		}
		out = strings.TrimRight(out, "-")
	}
	if out == "" {
		return fallbackSlug
	}
	return out
}

// validRefName applies git check-ref-format rules to a branch name (the part
// after refs/heads/), plus a length cap and a ban on a leading "refs/".
func validRefName(name string) error {
	bad := func(why string) error { return fmt.Errorf("%w: %q (%s)", ErrInvalidBranchName, name, why) }
	switch {
	case name == "" || name == "@":
		return bad("empty")
	case len(name) > maxRefLen:
		return bad("too long")
	case strings.HasPrefix(name, "refs/"):
		return bad("pass the short branch name, not refs/...")
	case strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") || strings.HasSuffix(name, "."):
		return bad("leading/trailing slash or trailing dot")
	case strings.Contains(name, "..") || strings.Contains(name, "//") || strings.Contains(name, "@{"):
		return bad(`contains "..", "//" or "@{"`)
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(" ~^:?*[\\", r) {
			return bad("contains a control character, space or one of ~^:?*[\\")
		}
	}
	for _, comp := range strings.Split(name, "/") {
		if strings.HasPrefix(comp, ".") || strings.HasSuffix(comp, ".lock") {
			return bad(`a component starts with "." or ends with ".lock"`)
		}
	}
	return nil
}
