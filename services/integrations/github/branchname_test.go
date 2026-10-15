package github

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const testULID = "01J9ZKX3M4Q7R8S9T0V1W2X3Y4"

func TestValidULID(t *testing.T) {
	for _, s := range []string{testULID, "00000000000000000000000000", "7ZZZZZZZZZZZZZZZZZZZZZZZZZ"} {
		if !ValidULID(s) {
			t.Errorf("%q should be valid", s)
		}
	}
	for _, s := range []string{
		"", "01J9ZKX3M4Q7R8S9T0V1W2X3Y", testULID + "0", // length
		strings.ToLower(testULID),    // lowercase is not canonical
		"8ZZZZZZZZZZZZZZZZZZZZZZZZZ", // overflows 128 bits
		"01J9ZKX3M4Q7R8S9T0V1W2X3YI", // I
		"01J9ZKX3M4Q7R8S9T0V1W2X3YL", // L
		"01J9ZKX3M4Q7R8S9T0V1W2X3YO", // O
		"01J9ZKX3M4Q7R8S9T0V1W2X3YU", // U
		"01J9ZKX3M4Q7R8S9T0V1W2X3Y-",
	} {
		if ValidULID(s) {
			t.Errorf("%q should be invalid", s)
		}
	}
}

func TestValidSlug(t *testing.T) {
	for _, s := range []string{"a", "fix-checkout", "v2-api", strings.Repeat("a", MaxSlugLen)} {
		if !ValidSlug(s) {
			t.Errorf("%q should be valid", s)
		}
	}
	for _, s := range []string{"", "-a", "a-", "a--b", "Fix", "a_b", "a.b", "a/b", "café", strings.Repeat("a", MaxSlugLen+1)} {
		if ValidSlug(s) {
			t.Errorf("%q should be invalid", s)
		}
	}
}

func TestParseBranchName(t *testing.T) {
	u, s, err := ParseBranchName("alpine/" + testULID + "-fix-checkout")
	if err != nil || u != testULID || s != "fix-checkout" {
		t.Fatalf("%q %q %v", u, s, err)
	}
	for _, n := range []string{
		"alpine/" + testULID, "alpine/" + testULID + "-", "alpine/" + testULID + "_x",
		"alpine/" + strings.ToLower(testULID) + "-x", "alpine/t-1", "feature/" + testULID + "-x",
		"alpine/" + testULID + "-x/y", "alpine/" + testULID + "-x.lock", "",
	} {
		if err := ValidateBranchName(n); !errors.Is(err, ErrInvalidBranchName) {
			t.Errorf("%q: %v", n, err)
		}
	}
}

func TestNewBranchName(t *testing.T) {
	for _, tc := range []struct{ ulid, title, want string }{
		{testULID, "Fix checkout total (EUR)", "alpine/" + testULID + "-fix-checkout-total-eur"},
		{strings.ToLower(testULID), "Crash in /api/v2/users", "alpine/" + testULID + "-crash-in-api-v2-users"},
		{testULID, "Café crème: Straße ÆØ", "alpine/" + testULID + "-cafe-creme-strasse-aeo"},
		{testULID, "  --  ", "alpine/" + testULID + "-ticket"},
		{testULID, "数据库超时", "alpine/" + testULID + "-ticket"},
		{testULID, "TypeError: can't read 'x' of undefined", "alpine/" + testULID + "-typeerror-can-t-read-x-of-undefined"},
	} {
		got, err := NewBranchName(tc.ulid, tc.title)
		if err != nil || got != tc.want {
			t.Errorf("NewBranchName(%q, %q) = %q, %v; want %q", tc.ulid, tc.title, got, err, tc.want)
		}
		if err := ValidateBranchName(got); err != nil || validRefName(got) != nil {
			t.Errorf("%q not valid: %v", got, err)
		}
	}
	if _, err := NewBranchName("not-a-ulid", "x"); !errors.Is(err, ErrInvalidBranchName) {
		t.Fatal(err)
	}
}

func TestSlugifyLength(t *testing.T) {
	// Cut at a word boundary when one is in the second half of the cap.
	long := strings.Repeat("word ", 20)
	if s := Slugify(long); len(s) > MaxSlugLen || !ValidSlug(s) || strings.HasSuffix(s, "-wo") || s != strings.TrimSuffix(strings.Repeat("word-", 9), "-") {
		t.Fatalf("%q (%d)", s, len(s))
	}
	// One huge word is hard-cut.
	if s := Slugify(strings.Repeat("x", 100)); s != strings.Repeat("x", MaxSlugLen) {
		t.Fatalf("%q", s)
	}
	// A cut that lands on a separator does not leave a trailing hyphen.
	if s := Slugify(strings.Repeat("a", MaxSlugLen-1) + " bcd"); s != strings.Repeat("a", MaxSlugLen-1) {
		t.Fatalf("%q", s)
	}
	// Every output is a valid slug.
	for _, in := range []string{"", "A", "ÀÉÎõü", "a-b--c", "-x-", "1.2.3", "🚀 launch", strings.Repeat("ab-", 40)} {
		if s := Slugify(in); !ValidSlug(s) {
			t.Errorf("Slugify(%q) = %q", in, s)
		}
	}
}

func TestValidRefName(t *testing.T) {
	for _, n := range []string{"main", "release/1.2", "feature/x_y", "alpine/" + testULID + "-x", "a@b"} {
		if err := validRefName(n); err != nil {
			t.Errorf("%q: %v", n, err)
		}
	}
	for _, n := range []string{
		"", "@", "refs/heads/x", "/x", "x/", "x.", "a..b", "a//b", "a@{1}", "a b", "a~1", "a^", "a:b",
		"a?", "a*", "a[b", `a\b`, "a\x01b", "a\x7fb", ".hidden", "x/.y", "x.lock", "x/y.lock/z",
		strings.Repeat("a", maxRefLen+1),
	} {
		if err := validRefName(n); !errors.Is(err, ErrInvalidBranchName) {
			t.Errorf("%q: %v", n, err)
		}
	}
}

func TestBranchFormatEnforcedOnWrites(t *testing.T) {
	ctx := context.Background()
	f := newFake(t, map[string]route{})
	c := newRepoClient(t, f)
	bad := "alpine/t-1"
	if _, err := c.CreateBranch(ctx, bad, "sha"); !errors.Is(err, ErrInvalidBranchName) {
		t.Error(err)
	}
	if _, err := c.CreateBranchFrom(ctx, bad, "main"); !errors.Is(err, ErrInvalidBranchName) {
		t.Error(err)
	}
	if err := c.UpdateRef(ctx, bad, "sha", false); !errors.Is(err, ErrInvalidBranchName) {
		t.Error(err)
	}
	if _, err := c.PushFiles(ctx, Push{Branch: bad, Message: "m", Changes: []FileChange{{Path: "a", Content: []byte("x")}}}); !errors.Is(err, ErrInvalidBranchName) {
		t.Error(err)
	}
	// Even when any branch is writable, alpine/* must follow the format.
	open, err := NewClient(ClientConfig{BaseURL: f.URL, Tokens: StaticToken("x"), Owner: "acme", Repo: "shop", WritableBranchPrefixes: []string{}, Sleep: noSleep})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := open.CreateBranch(ctx, bad, "sha"); !errors.Is(err, ErrInvalidBranchName) {
		t.Error(err)
	}
	if _, err := open.CreateBranch(ctx, "bad..name", "sha"); !errors.Is(err, ErrInvalidBranchName) {
		t.Error(err)
	}
	// Invalid base for CreateBranchFrom is rejected before it is read.
	f2 := newFake(t, map[string]route{"GET /repos/acme/shop/git/ref/heads/alpine/" + testULID + "-x": {status: 404, body: `{}`}})
	if _, err := newRepoClient(t, f2).CreateBranchFrom(ctx, "alpine/"+testULID+"-x", "ma:in"); !errors.Is(err, ErrInvalidBranchName) {
		t.Error(err)
	}
	if n := len(f.requests()); n != 0 {
		t.Fatalf("requests %d", n)
	}
}
