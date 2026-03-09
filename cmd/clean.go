package cmd

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	hauntSSH "github.com/sch0tten/hauntty/ssh"
	"github.com/spf13/cobra"
)

var cleanCmd = &cobra.Command{
	Use:   "clean",
	Short: "Remove stale session sockets and optionally remote session data",
	Long:  "Removes local stale socket symlinks that point to dead daemons. With --remote, also cleans session directories on remote hosts.",
	RunE: func(cmd *cobra.Command, args []string) error {
		// Find all local hauntty sockets
		socks, err := filepath.Glob("/tmp/hauntty-*.sock")
		if err != nil {
			return err
		}

		alive := 0
		removed := 0
		for _, sock := range socks {
			// Extract SID from socket name
			base := filepath.Base(sock)
			sid := strings.TrimPrefix(base, "hauntty-")
			sid = strings.TrimSuffix(sid, ".sock")

			// Try connecting — if it works, the daemon is alive
			conn, err := net.DialTimeout("unix", sock, 500*time.Millisecond)
			if err != nil {
				os.Remove(sock)
				hauntSSH.KillTunnel(sid)
				fmt.Printf("removed stale socket: %s (%s)\n", sid, sock)
				removed++
				continue
			}
			conn.Close()
			alive++
		}

		if removed == 0 && alive == 0 {
			fmt.Println("no sessions found")
		} else if removed == 0 {
			fmt.Printf("%d active session(s), nothing to clean\n", alive)
		} else {
			fmt.Printf("removed %d stale socket(s), %d active session(s) remaining\n", removed, alive)
		}
		return nil
	},
}
