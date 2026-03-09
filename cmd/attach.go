package cmd

import (
	"fmt"
	"io"
	"os"

	"github.com/sch0tten/hauntty/protocol"
	"github.com/spf13/cobra"
)

var attachTarget string

var attachCmd = &cobra.Command{
	Use:               "attach <session-id>",
	Short:             "Spectate a session in real time (read-only)",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeSIDArg,
	RunE: func(cmd *cobra.Command, args []string) error {
		sid := args[0]
		conn, err := connectSession(sid)
		if err != nil {
			return err
		}
		defer conn.Close()

		// Send watch request with optional target session
		enc := protocol.NewEncoder(conn)
		if err := enc.Encode(&protocol.Request{Op: protocol.OpWatch, SID: attachTarget}); err != nil {
			return fmt.Errorf("send watch: %w", err)
		}

		targetDesc := sid
		if attachTarget != "" {
			targetDesc = attachTarget
		}
		fmt.Fprintf(os.Stderr, "attached to session %s (read-only, Ctrl-C to detach)\n", targetDesc)

		// Stream PTY output to terminal
		_, err = io.Copy(os.Stdout, conn)
		return err
	},
}

func init() {
	attachCmd.Flags().StringVar(&attachTarget, "target", "", "Target session ID to watch (default: primary)")
}
