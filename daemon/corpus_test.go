package daemon

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAppendCorpusEntry(t *testing.T) {
	dir := t.TempDir()
	sess := &Session{
		SID:      "abc12345",
		Hostname: "testhost",
		User:     "testuser",
		IP:       "192.168.1.100",
	}

	err := AppendCorpusEntry(dir, sess, 1, "echo hello", 0, "/home/test", 1, 0, 150*time.Millisecond)
	if err != nil {
		t.Fatalf("first write: %v", err)
	}

	err = AppendCorpusEntry(dir, sess, 2, "false", 1, "/home/test", 0, 1, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("second write: %v", err)
	}

	// Read back and verify
	corpusPath := filepath.Join(dir, "corpus.jsonl")
	f, err := os.Open(corpusPath)
	if err != nil {
		t.Fatalf("open corpus: %v", err)
	}
	defer f.Close()

	var entries []CorpusEntry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var e CorpusEntry
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		entries = append(entries, e)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}

	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	// Verify first entry
	e := entries[0]
	if e.SID != "abc12345" {
		t.Errorf("sid: got %q, want %q", e.SID, "abc12345")
	}
	if e.Host != "testhost" {
		t.Errorf("host: got %q, want %q", e.Host, "testhost")
	}
	if e.User != "testuser" {
		t.Errorf("user: got %q, want %q", e.User, "testuser")
	}
	if e.IP != "192.168.1.100" {
		t.Errorf("ip: got %q, want %q", e.IP, "192.168.1.100")
	}
	if e.Seq != 1 {
		t.Errorf("seq: got %d, want 1", e.Seq)
	}
	if e.Cmd != "echo hello" {
		t.Errorf("cmd: got %q, want %q", e.Cmd, "echo hello")
	}
	if e.RC != 0 {
		t.Errorf("rc: got %d, want 0", e.RC)
	}
	if e.CWD != "/home/test" {
		t.Errorf("cwd: got %q, want %q", e.CWD, "/home/test")
	}
	if e.StdoutLines != 1 {
		t.Errorf("stdout_lines: got %d, want 1", e.StdoutLines)
	}
	if e.StderrLines != 0 {
		t.Errorf("stderr_lines: got %d, want 0", e.StderrLines)
	}
	if e.ElapsedS < 0.1 || e.ElapsedS > 0.2 {
		t.Errorf("elapsed_s: got %f, want ~0.15", e.ElapsedS)
	}

	// Verify timestamp is valid RFC3339
	if _, err := time.Parse(time.RFC3339Nano, e.Timestamp); err != nil {
		t.Errorf("timestamp not valid RFC3339Nano: %q", e.Timestamp)
	}

	// Verify second entry
	e2 := entries[1]
	if e2.Seq != 2 || e2.RC != 1 || e2.Cmd != "false" {
		t.Errorf("second entry: seq=%d rc=%d cmd=%q", e2.Seq, e2.RC, e2.Cmd)
	}
}

func TestAppendCorpusEntryEmptyIP(t *testing.T) {
	dir := t.TempDir()
	sess := &Session{
		SID:      "def67890",
		Hostname: "localbox",
		User:     "dev",
	}

	err := AppendCorpusEntry(dir, sess, 1, "ls", 0, "/tmp", 5, 0, time.Second)
	if err != nil {
		t.Fatalf("write: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, "corpus.jsonl"))
	var e CorpusEntry
	json.Unmarshal(data, &e)

	if e.IP != "" {
		t.Errorf("expected empty IP with omitempty, got %q", e.IP)
	}
}

func TestAppendCorpusEntryCreatesFile(t *testing.T) {
	dir := t.TempDir()
	corpusPath := filepath.Join(dir, "corpus.jsonl")

	// File should not exist yet
	if _, err := os.Stat(corpusPath); !os.IsNotExist(err) {
		t.Fatal("corpus.jsonl should not exist before first write")
	}

	sess := &Session{SID: "aaa", Hostname: "h", User: "u"}
	AppendCorpusEntry(dir, sess, 1, "pwd", 0, "/", 1, 0, time.Millisecond)

	if _, err := os.Stat(corpusPath); err != nil {
		t.Fatalf("corpus.jsonl should exist after write: %v", err)
	}
}
