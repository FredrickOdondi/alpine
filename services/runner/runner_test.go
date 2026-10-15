package runner

import "testing"

func TestCappedBuffer(t *testing.T) {
	b := &CappedBuffer{Limit: 5}
	n, _ := b.Write([]byte("abc"))
	if n != 3 || b.Truncated {
		t.Fatal("unexpected")
	}
	b.Write([]byte("defg"))
	b.Write([]byte("h"))
	if string(b.Bytes()) != "abcde" || !b.Truncated {
		t.Fatalf("got %q %v", b.Bytes(), b.Truncated)
	}
}
