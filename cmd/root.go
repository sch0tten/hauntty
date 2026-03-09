package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var (
	Version   string
	Commit    string
	BuildDate string
)

var rootCmd = &cobra.Command{
	Use:   "hauntty",
	Short: "Persistent shell sessions for LLM agents",
	Long:  "hauntty gives LLM agents persistent, observable shell sessions on remote hosts.",
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		// Skip for completion commands to avoid recursion
		if cmd.Name() == "completion" || cmd.Name() == "__complete" {
			return
		}
		ensureCompletions(cmd.Root())
	},
}

func Execute() error {
	return rootCmd.Execute()
}

func init() {
	rootCmd.AddCommand(daemonCmd)
	rootCmd.AddCommand(execCmd)
	rootCmd.AddCommand(pollCmd)
	rootCmd.AddCommand(readCmd)
	rootCmd.AddCommand(peekCmd)
	rootCmd.AddCommand(attachCmd)
	rootCmd.AddCommand(listCmd)
	rootCmd.AddCommand(killCmd)
	rootCmd.AddCommand(cleanCmd)
	rootCmd.AddCommand(uninstallCmd)
	rootCmd.AddCommand(spawnCmd)
	rootCmd.AddCommand(corpusCmd)
	rootCmd.AddCommand(versionCmd)
}

// VersionString returns the full version string for display and comparison.
func VersionString() string {
	if Version == "dev" {
		return "hauntty dev"
	}
	return fmt.Sprintf("hauntty %s (%s, %s)", Version, Commit, BuildDate)
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version",
	Run: func(cmd *cobra.Command, args []string) {
		cmd.Println(VersionString())
	},
}
