package main

import (
	"slices"
	"testing"
)

func TestGatesExtraEnv(t *testing.T) {
	got := gatesExtraEnv([]string{
		"DATABASE_URL=postgres://secret",
		"GATES_ENV_PLAYWRIGHT_BASE_URL=http://localhost:3000",
		"GATES_ENV_TOKEN=a=b",
		"GATES_ENV_=ignored",
		"OPENAI_API_KEY=sk-nope",
	})
	want := []string{"PLAYWRIGHT_BASE_URL=http://localhost:3000", "TOKEN=a=b"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
