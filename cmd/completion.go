package cmd

import (
	"net"
	"path/filepath"
	"strings"

	"github.com/sch0tten/hauntty/protocol"
	"github.com/spf13/cobra"
)

// completeSessionIDs returns live session IDs for shell completion.
func completeSessionIDs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	matches, _ := filepath.Glob("/tmp/hauntty-*.sock")
	var sids []string
	for _, sockPath := range matches {
		base := filepath.Base(sockPath)
		sid := strings.TrimPrefix(base, "hauntty-")
		sid = strings.TrimSuffix(sid, ".sock")

		// Verify the session is alive by connecting
		conn, err := net.Dial("unix", sockPath)
		if err != nil {
			continue
		}

		resp, err := protocol.SendRequest(conn, &protocol.Request{Op: protocol.OpList})
		conn.Close()
		if err != nil {
			continue
		}

		for _, s := range resp.Sessions {
			desc := s.SID + "\t" + s.User + "@" + s.Host + ":" + s.CWD
			if strings.HasPrefix(s.SID, toComplete) {
				sids = append(sids, desc)
			}
		}
	}
	return sids, cobra.ShellCompDirectiveNoFileComp
}

// completeStreamNames completes stdout/stderr for --stream flag.
func completeStreamNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return []string{"stdout\tstandard output", "stderr\tstandard error"}, cobra.ShellCompDirectiveNoFileComp
}

// completeSIDArg is for commands that take session ID as a positional arg.
var completeSIDArg = func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return completeSessionIDs(cmd, args, toComplete)
	}
	return nil, cobra.ShellCompDirectiveNoFileComp
}
