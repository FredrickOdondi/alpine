package config

import "testing"

func TestLoadGitHub(t *testing.T) {
	t.Setenv("GITHUB_APP_ID", "12345")
	t.Setenv("GITHUB_APP_BOT_LOGIN", "alpine-dev[bot]")
	g, err := LoadGitHub()
	if err != nil || g.AppID != 12345 || g.BotLogin != "alpine-dev[bot]" || g.Live {
		t.Fatal(g, err)
	}
	t.Setenv("GITHUB_APP_BOT_LOGIN", "alpine-dev")
	if _, err := LoadGitHub(); err == nil {
		t.Fatal("bot login without [bot] must fail")
	}
	t.Setenv("GITHUB_APP_BOT_LOGIN", "alpine-dev[bot]")
	t.Setenv("GITHUB_LIVE", "true")
	if _, err := LoadGitHub(); err == nil {
		t.Fatal("live without private key must fail")
	}
	t.Setenv("GITHUB_APP_ID", "x")
	t.Setenv("GITHUB_LIVE", "")
	if _, err := LoadGitHub(); err == nil {
		t.Fatal("bad app id must fail")
	}
}
