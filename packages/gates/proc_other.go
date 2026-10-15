//go:build !unix

package gates

import "os/exec"

// configureKill falls back to killing only the direct child (default
// exec.CommandContext behaviour) on non-unix platforms.
func configureKill(cmd *exec.Cmd) {}
