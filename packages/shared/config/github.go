package config

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/alpine-dev/alpine/packages/shared/llm"
)

// GitHub is the GitHub App configuration. AppID and BotLogin identify Alpine's own App
// ("<app-slug>[bot]"); the integrations client uses them to ignore Alpine-authored events and
// comments (loop prevention).
type GitHub struct {
	AppID          int64
	BotLogin       string
	PrivateKeyFile string
	WebhookSecret  llm.Secret
	APIBaseURL     string
	// Live enables real GitHub writes (branches/PRs/comments). Off by default: the worker uses the
	// stub SCM so local runs never touch GitHub.
	Live bool
}

// LoadGitHub reads GITHUB_* env vars.
func LoadGitHub() (GitHub, error) {
	g := GitHub{
		BotLogin:       String("GITHUB_APP_BOT_LOGIN", ""),
		PrivateKeyFile: String("GITHUB_APP_PRIVATE_KEY_FILE", ""),
		WebhookSecret:  llm.Secret(String("GITHUB_WEBHOOK_SECRET", "")),
		APIBaseURL:     String("GITHUB_API_URL", "https://api.github.com"),
		Live:           Bool("GITHUB_LIVE", false),
	}
	if v := String("GITHUB_APP_ID", ""); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			return g, fmt.Errorf("config: GITHUB_APP_ID must be a positive integer")
		}
		g.AppID = id
	}
	if g.BotLogin != "" && !strings.HasSuffix(g.BotLogin, "[bot]") {
		return g, fmt.Errorf("config: GITHUB_APP_BOT_LOGIN must be \"<app-slug>[bot]\", got %q", g.BotLogin)
	}
	if g.Live && (g.AppID == 0 || g.BotLogin == "" || g.PrivateKeyFile == "") {
		return g, fmt.Errorf("config: GITHUB_LIVE=true requires GITHUB_APP_ID, GITHUB_APP_BOT_LOGIN and GITHUB_APP_PRIVATE_KEY_FILE")
	}
	return g, nil
}
