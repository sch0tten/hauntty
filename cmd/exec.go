package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/sch0tten/hauntty/protocol"
	"github.com/spf13/cobra"
)

var execSession string
var execTarget string
var execWait bool
var execTimeout time.Duration
var execYes bool

var execCmd = &cobra.Command{
	Use:   "exec [command...]",
	Short: "Execute a command in a session",
	Long:  "Execute a command in a persistent hauntty session. Returns the sequence number.",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		conn, err := connectSession(execSession)
		if err != nil {
			return err
		}
		defer conn.Close()

		fullCmd := strings.Join(args, " ")
		enc := protocol.NewEncoder(conn)
		dec := protocol.NewDecoder(conn)

		// Send exec request
		if err := enc.Encode(&protocol.Request{
			Op:             protocol.OpExec,
			Cmd:            fullCmd,
			SID:            execTarget,
			NonInteractive: execYes,
		}); err != nil {
			return fmt.Errorf("send exec: %w", err)
		}

		// Read ack
		var ack protocol.Response
		if err := dec.Decode(&ack); err != nil {
			return fmt.Errorf("read ack: %w", err)
		}
		if ack.Op == protocol.OpError {
			return fmt.Errorf("exec error: %s", ack.Error)
		}

		seq := ack.Seq
		fmt.Printf("seq: %d\n", seq)
		fmt.Printf("cmd: %s\n", fullCmd)

		if !execWait {
			return nil
		}

		// Wait for done message with a real deadline on the connection
		conn.SetReadDeadline(time.Now().Add(execTimeout))

		for {
			var resp protocol.Response
			if err := dec.Decode(&resp); err != nil {
				return fmt.Errorf("timeout or read error waiting for command %d: %w", seq, err)
			}
			if resp.Op == protocol.OpStatus {
				// Live status from /proc monitor
				fmt.Printf("\rstatus: %s  cpu=%.0f%%  io=%d  elapsed=%.0fs  pid=%d",
					resp.State, resp.CPU, resp.IOBytes, resp.Elapsed, resp.ChildPID)
				// Reset deadline on active states — command is making progress
				if resp.State != protocol.StateWaitingInput {
					conn.SetReadDeadline(time.Now().Add(execTimeout))
				}
				continue
			}
			if resp.Op == protocol.OpPrompt {
				fmt.Printf("\nprompt_detected: %s\n", resp.Prompt)
				if execYes {
					fmt.Println("auto_response: yes (--yes mode)")
				} else {
					fmt.Println("hint: command is waiting for input; re-run with --yes to auto-answer, or use 'hauntty attach' for interactive access")
				}
				continue // keep waiting for OpDone
			}
			if resp.Op == protocol.OpDone {
				fmt.Print("\r\033[K") // clear status line
				rc := 0
				if resp.RC != nil {
					rc = *resp.RC
				}
				fmt.Printf("rc: %d\n", rc)
				fmt.Printf("stdout_lines: %d\n", resp.StdoutLines)
				fmt.Printf("stderr_lines: %d\n", resp.StderrLines)
				fmt.Printf("cwd: %s\n", resp.CWD)
				if resp.Error != "" {
					fmt.Printf("error: %s\n", resp.Error)
				}

				// Auto-read stdout on same connection
				conn.SetReadDeadline(time.Now().Add(5 * time.Second))
				if resp.StdoutLines > 0 && resp.StdoutLines <= 100 {
					if err := enc.Encode(&protocol.Request{
						Op:     protocol.OpRead,
						Seq:    seq,
						SID:    execTarget,
						Stream: "stdout",
						Limit:  100,
					}); err == nil {
						var readResp protocol.Response
						if err := dec.Decode(&readResp); err == nil && readResp.Op == protocol.OpData {
							fmt.Println("---")
							for _, line := range readResp.DataLines {
								fmt.Println(line)
							}
						}
					}
				}
				return nil
			}
		}
	},
}

func init() {
	execCmd.Flags().StringVarP(&execSession, "session", "s", "", "Session ID (required)")
	execCmd.MarkFlagRequired("session")
	execCmd.RegisterFlagCompletionFunc("session", completeSessionIDs)
	execCmd.Flags().StringVar(&execTarget, "target", "", "Target session ID within daemon (default: primary)")
	execCmd.Flags().BoolVarP(&execWait, "wait", "w", true, "Wait for command to complete")
	execCmd.Flags().DurationVarP(&execTimeout, "timeout", "t", 5*time.Minute, "Timeout for --wait")
	execCmd.Flags().BoolVarP(&execYes, "yes", "y", false, "Non-interactive mode: auto-answer prompts with 'yes'")
}
