package cmd

import (
	"fmt"
	"time"

	"github.com/sch0tten/hauntty/protocol"
	"github.com/spf13/cobra"
)

var (
	waitSession string
	waitTarget  string
	waitPID     int
	waitSeq     int
	waitTimeout time.Duration
)

var waitCmd = &cobra.Command{
	Use:   "wait",
	Short: "Block until a process exits or a command completes",
	Long: `Block until a target finishes — a deterministic gate instead of polling
with "sleep N" + "ssh host ps aux | grep". The daemon waits locally on the
remote host (against /proc or the command's exit-code file), so there are no
repeated SSH round-trips and no guessed intervals.

Examples:
  # A command left a background job running; gate on it deterministically:
  hauntty exec -s $SID "./long_job & echo started"   # prints background_pids: [12345]
  hauntty wait -s $SID --pid 12345                    # returns when pid 12345 exits

  # Wait for a previously-started command (by sequence number):
  hauntty wait -s $SID --seq 7`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if waitPID == 0 && waitSeq == 0 {
			return fmt.Errorf("specify --pid or --seq")
		}
		conn, err := connectSession(waitSession)
		if err != nil {
			return err
		}
		defer conn.Close()

		// Allow the client to wait a touch longer than the daemon deadline.
		conn.SetReadDeadline(time.Now().Add(waitTimeout + 10*time.Second))

		resp, err := protocol.SendRequest(conn, &protocol.Request{
			Op:       protocol.OpWait,
			SID:      waitTarget,
			WaitPID:  waitPID,
			Seq:      waitSeq,
			TimeoutS: int(waitTimeout.Seconds()),
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
		if resp.Error != "" {
			fmt.Printf("note: %s\n", resp.Error)
		}
		if resp.State == "timeout" {
			return fmt.Errorf("wait timed out")
		}
		return nil
	},
}

func init() {
	waitCmd.Flags().StringVarP(&waitSession, "session", "s", "", "Session ID")
	waitCmd.MarkFlagRequired("session")
	waitCmd.RegisterFlagCompletionFunc("session", completeSessionIDs)
	waitCmd.Flags().StringVar(&waitTarget, "target", "", "Target session ID within daemon (default: primary)")
	waitCmd.Flags().IntVar(&waitPID, "pid", 0, "Block until this PID exits")
	waitCmd.Flags().IntVar(&waitSeq, "seq", 0, "Block until this command sequence completes")
	waitCmd.Flags().DurationVar(&waitTimeout, "timeout", 10*time.Minute, "Maximum time to wait")
	rootCmd.AddCommand(waitCmd)
}
