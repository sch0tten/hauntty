package cmd

import (
	"fmt"
	"os"

	"github.com/sch0tten/hauntty/protocol"
	hauntSSH "github.com/sch0tten/hauntty/ssh"
	"github.com/spf13/cobra"
)

var killTarget string

var killCmd = &cobra.Command{
	Use:               "kill <session-id>",
	Short:             "Kill a session",
	Long:              "Kill a session by ID. The positional arg is the primary session (socket). Use --target to kill a spawned session instead.",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeSIDArg,
	RunE: func(cmd *cobra.Command, args []string) error {
		sid := args[0]
		conn, err := connectSession(sid)
		if err != nil {
			return err
		}
		defer conn.Close()

		// If target is specified, kill the spawned session; otherwise kill the primary
		killSID := sid
		if killTarget != "" {
			killSID = killTarget
		}

		resp, err := protocol.SendRequest(conn, &protocol.Request{
			Op:  protocol.OpKill,
			SID: killSID,
		})
		if err != nil {
			return err
		}

		if resp.Op == protocol.OpError {
			return fmt.Errorf("%s", resp.Error)
		}

		fmt.Printf("session %s killed\n", killSID)

		// Only remove socket and tunnel if we killed the primary (daemon shuts down)
		if killSID == sid {
			sockPath := fmt.Sprintf("/tmp/hauntty-%s.sock", sid)
			os.Remove(sockPath)
			hauntSSH.KillTunnel(sid)
		}
		return nil
	},
}

func init() {
	killCmd.Flags().StringVar(&killTarget, "target", "", "Target session ID to kill (default: primary, which shuts down daemon)")
}
