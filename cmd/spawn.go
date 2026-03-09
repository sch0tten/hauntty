package cmd

import (
	"fmt"

	"github.com/sch0tten/hauntty/protocol"
	"github.com/spf13/cobra"
)

var spawnSession string

var spawnCmd = &cobra.Command{
	Use:   "spawn",
	Short: "Spawn a new parallel session within the daemon",
	Long:  "Creates a new session (PTY + bash) within the same daemon. Each session has its own shell state, CWD, and log. Returns the new session ID.",
	RunE: func(cmd *cobra.Command, args []string) error {
		conn, err := connectSession(spawnSession)
		if err != nil {
			return err
		}
		defer conn.Close()

		resp, err := protocol.SendRequest(conn, &protocol.Request{
			Op: protocol.OpSpawn,
		})
		if err != nil {
			return err
		}
		if resp.Op == protocol.OpError {
			return fmt.Errorf("spawn: %s", resp.Error)
		}

		fmt.Printf("HAUNTTY_SID=%s\n", resp.SID)
		return nil
	},
}

func init() {
	spawnCmd.Flags().StringVarP(&spawnSession, "session", "s", "", "Primary session ID (to reach the daemon)")
	spawnCmd.MarkFlagRequired("session")
	spawnCmd.RegisterFlagCompletionFunc("session", completeSessionIDs)
}
