package cmd

import (
	"fmt"

	"github.com/sch0tten/hauntty/protocol"
	"github.com/spf13/cobra"
)

var pollSession string
var pollTarget string
var pollSeq int

var pollCmd = &cobra.Command{
	Use:   "poll",
	Short: "Poll for command completion",
	RunE: func(cmd *cobra.Command, args []string) error {
		conn, err := connectSession(pollSession)
		if err != nil {
			return err
		}
		defer conn.Close()

		resp, err := protocol.SendRequest(conn, &protocol.Request{
			Op:  protocol.OpPoll,
			Seq: pollSeq,
			SID: pollTarget,
		})
		if err != nil {
			return err
		}

		if resp.Op == protocol.OpError {
			return fmt.Errorf("%s", resp.Error)
		}

		fmt.Printf("state: %s\n", resp.State)
		if resp.RC != nil {
			fmt.Printf("rc: %d\n", *resp.RC)
		}
		return nil
	},
}

func init() {
	pollCmd.Flags().StringVarP(&pollSession, "session", "s", "", "Session ID")
	pollCmd.MarkFlagRequired("session")
	pollCmd.RegisterFlagCompletionFunc("session", completeSessionIDs)
	pollCmd.Flags().StringVar(&pollTarget, "target", "", "Target session ID within daemon (default: primary)")
	pollCmd.Flags().IntVar(&pollSeq, "seq", 0, "Command sequence number")
	pollCmd.MarkFlagRequired("seq")
}
