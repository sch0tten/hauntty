package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sch0tten/hauntty/daemon"
	"github.com/spf13/cobra"
)

var (
	corpusSince  string
	corpusHost   string
	corpusFailed bool
	corpusSID    string
)

var corpusCmd = &cobra.Command{
	Use:   "corpus",
	Short: "Query the corpus log",
	Long:  "Dump or filter the centralized command corpus (corpus.jsonl).",
	RunE: func(cmd *cobra.Command, args []string) error {
		baseDir := daemon.DefaultBaseDir()
		corpusPath := filepath.Join(baseDir, "corpus.jsonl")

		f, err := os.Open(corpusPath)
		if err != nil {
			if os.IsNotExist(err) {
				fmt.Println("no corpus log found")
				return nil
			}
			return err
		}
		defer f.Close()

		// Parse --since into a cutoff time
		var cutoff time.Time
		if corpusSince != "" {
			dur, err := time.ParseDuration(corpusSince)
			if err != nil {
				return fmt.Errorf("invalid --since duration: %w (use e.g. 1h, 30m, 24h)", err)
			}
			cutoff = time.Now().UTC().Add(-dur)
		}

		scanner := bufio.NewScanner(f)
		// Allow up to 1MB lines (large entries)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

		for scanner.Scan() {
			var entry daemon.CorpusEntry
			if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
				continue // skip malformed lines
			}

			// Apply filters
			if corpusHost != "" && entry.Host != corpusHost {
				continue
			}
			if corpusSID != "" && entry.SID != corpusSID {
				continue
			}
			if corpusFailed && entry.RC == 0 {
				continue
			}
			if !cutoff.IsZero() {
				ts, err := time.Parse(time.RFC3339Nano, entry.Timestamp)
				if err != nil || ts.Before(cutoff) {
					continue
				}
			}

			// Output the raw JSON line
			fmt.Println(scanner.Text())
		}

		return scanner.Err()
	},
}

func init() {
	corpusCmd.Flags().StringVar(&corpusSince, "since", "", "Show entries from the last duration (e.g. 1h, 30m, 24h)")
	corpusCmd.Flags().StringVar(&corpusHost, "host", "", "Filter by hostname")
	corpusCmd.Flags().StringVar(&corpusSID, "sid", "", "Filter by session ID")
	corpusCmd.Flags().BoolVar(&corpusFailed, "failed", false, "Show only failed commands (rc != 0)")
}
