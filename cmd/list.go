package cmd

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/sch0tten/hauntty/protocol"
	"github.com/spf13/cobra"
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List active sessions",
	RunE: func(cmd *cobra.Command, args []string) error {
		// Find all hauntty sockets
		matches, _ := filepath.Glob("/tmp/hauntty-*.sock")
		if len(matches) == 0 {
			fmt.Println("no active sessions")
			return nil
		}

		for _, sockPath := range matches {
			// Extract SID from socket path
			base := filepath.Base(sockPath)
			sid := strings.TrimPrefix(base, "hauntty-")
			sid = strings.TrimSuffix(sid, ".sock")

			conn, err := net.Dial("unix", sockPath)
			if err != nil {
				// Stale socket
				fmt.Printf("%-10s  (stale, removed)\n", sid)
				os.Remove(sockPath)
				continue
			}

			resp, err := protocol.SendRequest(conn, &protocol.Request{Op: protocol.OpList})
			conn.Close()

			if err != nil {
				fmt.Printf("%-10s  (error: %v)\n", sid, err)
				continue
			}

			// Collect dead non-primary sessions for pruning
			var deadSIDs []string
			for _, s := range resp.Sessions {
				marker := "  "
				if s.Primary {
					marker = "* "
				}
				status := "alive"
				if !s.Alive {
					status = "dead"
					if !s.Primary {
						deadSIDs = append(deadSIDs, s.SID)
					}
				}
				identity := s.User + "@" + s.Host
				if s.IP != "" {
					identity += " (" + s.IP + ")"
				}
				fmt.Printf("%s%-10s  %-35s  seq=%-4d  [%s]  cwd=%s  created=%s\n",
					marker, s.SID, identity, s.LastSeq, status, s.CWD, s.Created)
			}

			// Auto-prune dead spawned sessions
			for _, deadSID := range deadSIDs {
				pruneConn, err := net.Dial("unix", sockPath)
				if err != nil {
					continue
				}
				resp, err := protocol.SendRequest(pruneConn, &protocol.Request{
					Op:  protocol.OpKill,
					SID: deadSID,
				})
				pruneConn.Close()
				if err == nil && resp.Op != protocol.OpError {
					fmt.Printf("  pruned dead session: %s\n", deadSID)
				}
			}
		}
		return nil
	},
}
