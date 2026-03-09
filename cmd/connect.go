package cmd

import (
	"fmt"

	hauntSSH "github.com/sch0tten/hauntty/ssh"
	"github.com/spf13/cobra"
)

var connectBinary string

var connectCmd = &cobra.Command{
	Use:   "connect <user@host>",
	Short: "Connect to a remote host (auto-deploy + start daemon + forward socket)",
	Long: `Connect to a remote host. This will:
  1. Check if hauntty is deployed on the remote host
  2. Deploy (scp) the binary if missing or version mismatch
  3. Start the daemon if not already running
  4. Set up SSH unix socket forwarding
  5. Print the session ID and local socket path`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		host := args[0]

		result, err := hauntSSH.Bootstrap(host, connectBinary)
		if err != nil {
			return err
		}

		fmt.Printf("HAUNTTY_SID=%s\n", result.SID)
		fmt.Printf("HAUNTTY_SOCK=%s\n", result.SockPath)
		fmt.Printf("HAUNTTY_HOST=%s\n", result.Host)
		return nil
	},
}

func init() {
	connectCmd.Flags().StringVar(&connectBinary, "binary", "", "Path to hauntty binary to deploy (default: self)")
	rootCmd.AddCommand(connectCmd)
}
