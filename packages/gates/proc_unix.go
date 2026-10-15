//go:build unix

package gates

import (
	"os/exec"
	"syscall"
)

// configureKill runs the command in its own process group and makes context
// cancellation (timeout) SIGKILL the whole group, so children spawned by
// `sh -c`, pnpm, npx etc. die too and don't keep the output pipes open.
func configureKill(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
