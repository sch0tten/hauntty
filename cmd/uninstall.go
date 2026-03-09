package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
)

var uninstallCmd = &cobra.Command{
	Use:   "uninstall <host>",
	Short: "Remove hauntty from a remote host",
	Long: `Stops any running hauntty daemons on the remote host and removes:
  - The hauntty binary (/tmp/hauntty)
  - All session data (~/.hauntty/)
  - Socket symlinks (/tmp/hauntty-*.sock)

Also cleans up the local socket symlinks for that host.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		host := args[0]

		fmt.Printf("stopping hauntty daemons on %s...\n", host)
		sshRun(host, "pkill -f 'hauntty daemon' 2>/dev/null; true")

		fmt.Printf("removing hauntty files on %s...\n", host)
		out, _ := sshRun(host, "rm -rf ~/.hauntty /tmp/hauntty /tmp/hauntty-*.sock 2>&1 && echo 'done'")
		if !strings.Contains(out, "done") {
			return fmt.Errorf("cleanup may have failed on %s: %s", host, out)
		}

		fmt.Printf("cleaning local sockets for %s...\n", host)
		// Run local clean to remove stale sockets
		cleanLocalSockets()

		fmt.Printf("hauntty removed from %s\n", host)
		return nil
	},
}

func sshRun(host, command string) (string, error) {
	cmd := exec.Command("ssh", host, command)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	return string(out), err
}

func cleanLocalSockets() {
	socks, _ := os.ReadDir("/tmp")
	for _, entry := range socks {
		if strings.HasPrefix(entry.Name(), "hauntty-") && strings.HasSuffix(entry.Name(), ".sock") {
			path := "/tmp/" + entry.Name()
			// Check if it's a dangling symlink
			if _, err := os.Stat(path); os.IsNotExist(err) {
				os.Remove(path)
			}
		}
	}
}
