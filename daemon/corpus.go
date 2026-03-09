package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// CorpusEntry represents a single completed command in the corpus log.
type CorpusEntry struct {
	SID         string  `json:"sid"`
	Host        string  `json:"host"`
	User        string  `json:"user"`
	IP          string  `json:"ip,omitempty"`
	Seq         int     `json:"seq"`
	Timestamp   string  `json:"ts"`
	CWD         string  `json:"cwd"`
	Cmd         string  `json:"cmd"`
	RC          int     `json:"rc"`
	StdoutLines int     `json:"stdout_lines"`
	StderrLines int     `json:"stderr_lines"`
	ElapsedS    float64 `json:"elapsed_s"`
}

// AppendCorpusEntry writes a command completion entry to the corpus log.
func AppendCorpusEntry(baseDir string, sess *Session, seq int, cmd string, rc int, cwd string, stdoutLines, stderrLines int, elapsed time.Duration) error {
	entry := CorpusEntry{
		SID:         sess.SID,
		Host:        sess.Hostname,
		User:        sess.User,
		IP:          sess.IP,
		Seq:         seq,
		Timestamp:   time.Now().UTC().Format(time.RFC3339Nano),
		CWD:         cwd,
		Cmd:         cmd,
		RC:          rc,
		StdoutLines: stdoutLines,
		StderrLines: stderrLines,
		ElapsedS:    elapsed.Seconds(),
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	corpusPath := filepath.Join(baseDir, "corpus.jsonl")
	f, err := os.OpenFile(corpusPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = f.Write(data)
	return err
}
