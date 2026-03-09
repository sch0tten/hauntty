package cmd

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/sch0tten/hauntty/daemon"
	"github.com/spf13/cobra"
)

var daemonBaseDir string
var daemonForeground bool

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Start the hauntty daemon",
	Long:  "Start the hauntty daemon, which manages persistent shell sessions.",
	RunE: func(cmd *cobra.Command, args []string) error {
		if daemonForeground || os.Getenv("HAUNTTY_DAEMON_CHILD") == "1" {
			// Run in foreground (either explicitly or we are the forked child)
			os.MkdirAll(daemonBaseDir, 0750)
			logFile := filepath.Join(daemonBaseDir, "hauntty.log")
			if f, err := os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); err == nil {
				log.SetOutput(f)
			}
			log.SetPrefix("hauntty: ")
			log.SetFlags(log.LstdFlags | log.Lshortfile)

			d := daemon.New(daemonBaseDir)
			return d.Start()
		}

		// Fork ourselves into the background
		exe, err := os.Executable()
		if err != nil {
			return fmt.Errorf("find executable: %w", err)
		}

		// Ensure base dir exists for the log file
		os.MkdirAll(daemonBaseDir, 0750)
		logFile := filepath.Join(daemonBaseDir, "hauntty.log")
		lf, err := os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			return fmt.Errorf("open log: %w", err)
		}

		child := exec.Command(exe, "daemon", "--base-dir", daemonBaseDir)
		child.Env = append(os.Environ(), "HAUNTTY_DAEMON_CHILD=1")
		child.Stdout = lf
		child.Stderr = lf
		child.Dir = "/"

		if err := child.Start(); err != nil {
			lf.Close()
			return fmt.Errorf("fork daemon: %w", err)
		}

		fmt.Printf("hauntty daemon started (pid %d)\n", child.Process.Pid)
		fmt.Printf("log: %s\n", logFile)

		// Detach — don't wait for child
		child.Process.Release()
		lf.Close()
		return nil
	},
}

func init() {
	daemonCmd.Flags().StringVar(&daemonBaseDir, "base-dir", daemon.DefaultBaseDir(), "Base directory for session data")
	daemonCmd.Flags().BoolVar(&daemonForeground, "foreground", false, "Run in foreground (don't daemonize)")
}
