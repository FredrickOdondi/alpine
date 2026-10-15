package gates

import (
	"context"
	"os/exec"
	"sort"
	"time"
)

var timeNow = time.Now

func sortStrings(s []string) { sort.Strings(s) }

// commandContext is exec.CommandContext with process-group kill on cancel.
func commandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	configureKill(cmd)
	cmd.WaitDelay = 3 * time.Second
	return cmd
}
