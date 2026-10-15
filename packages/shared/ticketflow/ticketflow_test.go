package ticketflow

import (
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestTicketULIDRoundTrip(t *testing.T) {
	for i := 0; i < 200; i++ {
		id := uuid.New()
		s := TicketULID(id)
		if len(s) != 26 {
			t.Fatalf("len %d", len(s))
		}
		back, err := ParseTicketULID(s)
		if err != nil || back != id {
			t.Fatalf("round trip %s -> %s -> %s (%v)", id, s, back, err)
		}
	}
	if TicketULID(uuid.Nil) != strings.Repeat("0", 26) {
		t.Fatal("nil")
	}
	max := uuid.UUID{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
	if TicketULID(max) != "7ZZZZZZZZZZZZZZZZZZZZZZZZZ" {
		t.Fatalf("max: %s", TicketULID(max))
	}
	if _, err := ParseTicketULID("8ZZZZZZZZZZZZZZZZZZZZZZZZZ"); err == nil {
		t.Fatal("overflow must fail")
	}
}

var branchRe = regexp.MustCompile(`^alpine/[0-7][0-9A-HJKMNP-TV-Z]{25}-[a-z0-9]+(-[a-z0-9]+)*$`)

func TestBranchName(t *testing.T) {
	id := uuid.MustParse("01890a5d-ac96-774b-bcce-b302099a8057")
	b := BranchName(id, "Login broken on Safari 17!!")
	if !branchRe.MatchString(b) || !strings.HasSuffix(b, "-login-broken-on-safari-17") {
		t.Fatalf("got %q", b)
	}
	if BranchName(id, "日本語") != "alpine/"+TicketULID(id)+"-ticket" {
		t.Fatal("empty slug fallback")
	}
	long := BranchName(id, strings.Repeat("abc ", 40))
	if !branchRe.MatchString(long) || len(long) > len("alpine/")+26+1+MaxSlugLen {
		t.Fatalf("long: %q", long)
	}
	if BranchName(id, "x") == BranchName(uuid.New(), "x") {
		t.Fatal("must be unique per ticket")
	}
}

func TestStepKeyAlphabet(t *testing.T) {
	k := StepKey(WorkflowID(uuid.NewString(), uuid.NewString()), "12")
	if !regexp.MustCompile(`^[A-Za-z0-9._:/@+=-]{1,128}$`).MatchString(k) || strings.Contains(k, "--") {
		t.Fatalf("bad key %q", k)
	}
}

func TestExternalKey(t *testing.T) {
	re := regexp.MustCompile(`^[A-Za-z0-9._:/@+=-]{1,128}$`)
	long := StepKey(WorkflowID(uuid.NewString(), uuid.NewString()), strings.Repeat("x--y ", 50))
	k := ExternalKey(long)
	if !re.MatchString(k) || strings.Contains(k, "--") || len(k) > 128 {
		t.Fatalf("bad key %q", k)
	}
	if ExternalKey(long) != k || ExternalKey(long+"1") == k {
		t.Fatal("must be stable and distinct")
	}
}

func TestSlugifyParity(t *testing.T) {
	cases := map[string]string{
		"Fix checkout total!":             "fix-checkout-total",
		"  --Hello__World--  ":            "hello-world",
		"":                                "ticket",
		strings.Repeat("abcdefghij ", 10): "abcdefghij-abcdefghij-abcdefghij-abcdefghij",
	}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q want %q", in, got, want)
		}
	}
}
