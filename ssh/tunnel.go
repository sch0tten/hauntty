package ssh

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// KillTunnel finds and kills the SSH tunnel process forwarding the given SID's socket.
func KillTunnel(sid string) error {
	sockPattern := fmt.Sprintf("/tmp/hauntty-%s.sock", sid)

	// Find SSH processes forwarding this socket
	// pgrep -f matches against the full command line
	out, err := exec.Command("pgrep", "-f", fmt.Sprintf("ssh.*%s", sockPattern)).Output()
	if err != nil {
		// pgrep exits 1 if no match — not an error for us
		return nil
	}

	pids := strings.Fields(strings.TrimSpace(string(out)))
	myPID := fmt.Sprintf("%d", os.Getpid())

	for _, pid := range pids {
		if pid == myPID {
			continue
		}
		// Verify this is actually an ssh -N tunnel (not some other process)
		cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%s/cmdline", pid))
		if err != nil {
			continue
		}
		cmd := string(cmdline)
		if !strings.Contains(cmd, "ssh") || !strings.Contains(cmd, "-N") {
			continue
		}
		proc, err := os.FindProcess(parseInt(pid))
		if err == nil {
			proc.Signal(os.Interrupt) // SIGINT for graceful shutdown
		}
	}
	return nil
}

func parseInt(s string) int {
	var n int
	fmt.Sscanf(s, "%d", &n)
	return n
}
