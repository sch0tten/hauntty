package cmd

import (
	"fmt"

	"github.com/sch0tten/hauntty/protocol"
	"github.com/spf13/cobra"
)

var peekSession string
var peekTarget string
var peekLines int

var peekCmd = &cobra.Command{
	Use:   "peek",
	Short: "Peek at live PTY output (last N lines)",
	RunE: func(cmd *cobra.Command, args []string) error {
		conn, err := connectSession(peekSession)
		if err != nil {
			return err
		}
		defer conn.Close()

		resp, err := protocol.SendRequest(conn, &protocol.Request{
			Op:    protocol.OpPeek,
			SID:   peekTarget,
			Lines: peekLines,
		})
		if err != nil {
			return err
		}

		if resp.Op == protocol.OpError {
			return fmt.Errorf("%s", resp.Error)
		}

		for _, line := range resp.DataLines {
			fmt.Println(line)
		}
		return nil
	},
}

func init() {
	peekCmd.Flags().StringVarP(&peekSession, "session", "s", "", "Session ID")
	peekCmd.MarkFlagRequired("session")
	peekCmd.RegisterFlagCompletionFunc("session", completeSessionIDs)
	peekCmd.Flags().StringVar(&peekTarget, "target", "", "Target session ID within daemon (default: primary)")
	peekCmd.Flags().IntVarP(&peekLines, "lines", "n", 20, "Number of lines to show")
}
