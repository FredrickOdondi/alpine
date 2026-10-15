package panicky

import "testing"

func init() { panic("init failed") }

func TestNever(t *testing.T) {}
