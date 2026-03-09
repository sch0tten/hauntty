package cmd

import (
	"fmt"

	"github.com/sch0tten/hauntty/protocol"
	"github.com/spf13/cobra"
)

var readSession string
var readTarget string
var readSeq int
var readStream string
var readOffset int
var readLimit int

var readCmd = &cobra.Command{
	Use:   "read",
	Short: "Read output from a completed command",
	RunE: func(cmd *cobra.Command, args []string) error {
		conn, err := connectSession(readSession)
		if err != nil {
			return err
		}
		defer conn.Close()

		enc := protocol.NewEncoder(conn)
		dec := protocol.NewDecoder(conn)

		// Read the command text first
		if err := enc.Encode(&protocol.Request{
			Op:     protocol.OpRead,
			Seq:    readSeq,
			SID:    readTarget,
			Stream: "cmdline",
		}); err != nil {
			return err
		}
		var cmdResp protocol.Response
		if err := dec.Decode(&cmdResp); err != nil {
			return err
		}
		if cmdResp.Op == protocol.OpData && len(cmdResp.DataLines) > 0 {
			fmt.Printf("seq: %d\n", readSeq)
			fmt.Printf("cmd: %s\n", cmdResp.DataLines[0])
			fmt.Printf("stream: %s\n", readStream)
			fmt.Println("---")
		}

		// Read the requested stream
		if err := enc.Encode(&protocol.Request{
			Op:     protocol.OpRead,
			Seq:    readSeq,
			SID:    readTarget,
			Stream: readStream,
			Offset: readOffset,
			Limit:  readLimit,
		}); err != nil {
			return err
		}
		var resp protocol.Response
		if err := dec.Decode(&resp); err != nil {
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
	readCmd.Flags().StringVarP(&readSession, "session", "s", "", "Session ID")
	readCmd.MarkFlagRequired("session")
	readCmd.RegisterFlagCompletionFunc("session", completeSessionIDs)
	readCmd.Flags().StringVar(&readTarget, "target", "", "Target session ID within daemon (default: primary)")
	readCmd.Flags().IntVar(&readSeq, "seq", 0, "Command sequence number")
	readCmd.MarkFlagRequired("seq")
	readCmd.Flags().StringVar(&readStream, "stream", "stdout", "Stream to read (stdout/stderr)")
	readCmd.RegisterFlagCompletionFunc("stream", completeStreamNames)
	readCmd.Flags().IntVar(&readOffset, "offset", 0, "Line offset")
	readCmd.Flags().IntVar(&readLimit, "limit", 0, "Max lines to return (0=all)")
}
