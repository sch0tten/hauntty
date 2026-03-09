package cmd

import (
	"fmt"
	"net"
	"os"
)

// connectSession dials the unix socket for a session and returns the connection.
// On failure, it gives a diagnostic error message with remediation hints.
func connectSession(sid string) (net.Conn, error) {
	sockPath := fmt.Sprintf("/tmp/hauntty-%s.sock", sid)

	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		// Check if socket file exists at all
		if _, statErr := os.Stat(sockPath); os.IsNotExist(statErr) {
			return nil, fmt.Errorf("session %s: socket not found — run 'hauntty list' to see active sessions or 'hauntty connect' to start a new one", sid)
		}
		// Socket exists but can't connect — likely dead tunnel or dead daemon
		return nil, fmt.Errorf("session %s: connection refused — the SSH tunnel or daemon may be down, try 'hauntty connect <host>' to re-establish", sid)
	}
	return conn, nil
}
