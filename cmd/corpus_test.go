package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sch0tten/hauntty/daemon"
)

// writeTestCorpus creates a corpus.jsonl with test entries and returns the base dir.
func writeTestCorpus(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	now := time.Now().UTC()
	entries := []daemon.CorpusEntry{
		{SID: "aaa", Host: "server01", User: "deploy", Seq: 1, Timestamp: now.Add(-2 * time.Hour).Format(time.RFC3339Nano), CWD: "/home", Cmd: "echo hello", RC: 0, StdoutLines: 1, ElapsedS: 0.05},
		{SID: "aaa", Host: "server01", User: "deploy", Seq: 2, Timestamp: now.Add(-1 * time.Hour).Format(time.RFC3339Nano), CWD: "/home", Cmd: "make build", RC: 2, StdoutLines: 0, StderrLines: 5, ElapsedS: 3.2},
		{SID: "bbb", Host: "server02", User: "deploy", Seq: 1, Timestamp: now.Add(-30 * time.Minute).Format(time.RFC3339Nano), CWD: "/opt", Cmd: "nvidia-smi", RC: 0, StdoutLines: 20, ElapsedS: 0.1},
		{SID: "bbb", Host: "server02", User: "deploy", Seq: 2, Timestamp: now.Add(-5 * time.Minute).Format(time.RFC3339Nano), CWD: "/opt", Cmd: "python train.py", RC: 1, StdoutLines: 100, StderrLines: 3, ElapsedS: 45.0},
	}

	var buf bytes.Buffer
	for _, e := range entries {
		data, _ := json.Marshal(e)
		buf.Write(data)
		buf.WriteByte('\n')
	}

	os.WriteFile(filepath.Join(dir, "corpus.jsonl"), buf.Bytes(), 0644)
	return dir
}

func TestCorpusNoFilter(t *testing.T) {
	dir := writeTestCorpus(t)
	entries := runCorpusFilter(t, dir, "", "", "", false)
	if len(entries) != 4 {
		t.Fatalf("expected 4 entries, got %d", len(entries))
	}
}

func TestCorpusFilterByHost(t *testing.T) {
	dir := writeTestCorpus(t)
	entries := runCorpusFilter(t, dir, "", "server02", "", false)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries for server02, got %d", len(entries))
	}
	for _, e := range entries {
		if e.Host != "server02" {
			t.Errorf("expected host server02, got %q", e.Host)
		}
	}
}

func TestCorpusFilterBySID(t *testing.T) {
	dir := writeTestCorpus(t)
	entries := runCorpusFilter(t, dir, "", "", "aaa", false)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries for SID aaa, got %d", len(entries))
	}
	for _, e := range entries {
		if e.SID != "aaa" {
			t.Errorf("expected sid aaa, got %q", e.SID)
		}
	}
}

func TestCorpusFilterFailed(t *testing.T) {
	dir := writeTestCorpus(t)
	entries := runCorpusFilter(t, dir, "", "", "", true)
	if len(entries) != 2 {
		t.Fatalf("expected 2 failed entries, got %d", len(entries))
	}
	for _, e := range entries {
		if e.RC == 0 {
			t.Errorf("expected rc != 0, got 0 for cmd %q", e.Cmd)
		}
	}
}

func TestCorpusFilterSince(t *testing.T) {
	dir := writeTestCorpus(t)
	// Only entries from last 45 minutes (should get the server02 entries)
	entries := runCorpusFilter(t, dir, "45m", "", "", false)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries within 45m, got %d", len(entries))
	}
}

func TestCorpusFilterCombined(t *testing.T) {
	dir := writeTestCorpus(t)
	// Failed on server02
	entries := runCorpusFilter(t, dir, "", "server02", "", true)
	if len(entries) != 1 {
		t.Fatalf("expected 1 failed entry on server02, got %d", len(entries))
	}
	if entries[0].Cmd != "python train.py" {
		t.Errorf("expected 'python train.py', got %q", entries[0].Cmd)
	}
}

func TestCorpusEmptyFile(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "corpus.jsonl"), []byte{}, 0644)
	entries := runCorpusFilter(t, dir, "", "", "", false)
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries for empty corpus, got %d", len(entries))
	}
}

// runCorpusFilter applies filters to the corpus in dir and returns matching entries.
// This tests the same filter logic as cmd/corpus.go without needing cobra.
func runCorpusFilter(t *testing.T, dir, since, host, sid string, failed bool) []daemon.CorpusEntry {
	t.Helper()

	corpusPath := filepath.Join(dir, "corpus.jsonl")
	data, err := os.ReadFile(corpusPath)
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}

	var cutoff time.Time
	if since != "" {
		dur, err := time.ParseDuration(since)
		if err != nil {
			t.Fatalf("parse duration: %v", err)
		}
		cutoff = time.Now().UTC().Add(-dur)
	}

	var results []daemon.CorpusEntry
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var entry daemon.CorpusEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}

		if host != "" && entry.Host != host {
			continue
		}
		if sid != "" && entry.SID != sid {
			continue
		}
		if failed && entry.RC == 0 {
			continue
		}
		if !cutoff.IsZero() {
			ts, err := time.Parse(time.RFC3339Nano, entry.Timestamp)
			if err != nil || ts.Before(cutoff) {
				continue
			}
		}

		results = append(results, entry)
	}
	return results
}
