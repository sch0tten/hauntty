package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// AppendSessionLog writes a command completion entry to the session log.
func AppendSessionLog(sessionDir string, seq int, cmd string, rc int, cwd string, stdoutLines, stderrLines int) error {
	logPath := filepath.Join(sessionDir, "session.log")
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	entry := fmt.Sprintf("---\nseq: %d\nts: %s\ncwd: %s\nrc: %d\ncmd: %s\nstdout_lines: %d\nstderr_lines: %d\n",
		seq,
		time.Now().UTC().Format(time.RFC3339Nano),
		cwd,
		rc,
		cmd,
		stdoutLines,
		stderrLines,
	)

	_, err = f.WriteString(entry)
	return err
}
