package cmd

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

// ensureCompletions installs shell completions on first run.
func ensureCompletions(root *cobra.Command) {
	shell := detectShell()
	if shell == "" {
		return
	}

	path := completionPath(shell)
	if path == "" {
		return
	}

	// Already installed
	if _, err := os.Stat(path); err == nil {
		return
	}

	var buf bytes.Buffer
	switch shell {
	case "bash":
		root.GenBashCompletionV2(&buf, true)
	case "zsh":
		root.GenZshCompletion(&buf)
	case "fish":
		root.GenFishCompletion(&buf, true)
	default:
		return
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return
	}

	if err := os.WriteFile(path, buf.Bytes(), 0644); err != nil {
		return
	}

	fmt.Fprintf(os.Stderr, "hauntty: shell completions installed to %s\n", path)
	fmt.Fprintf(os.Stderr, "hauntty: restart your shell or run: source %s\n", path)
}

func detectShell() string {
	shell := os.Getenv("SHELL")
	switch filepath.Base(shell) {
	case "bash":
		return "bash"
	case "zsh":
		return "zsh"
	case "fish":
		return "fish"
	}
	return ""
}

func completionPath(shell string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	switch shell {
	case "bash":
		// Standard user-local bash completion dir
		return filepath.Join(home, ".local", "share", "bash-completion", "completions", "hauntty")
	case "zsh":
		return filepath.Join(home, ".zsh", "completions", "_hauntty")
	case "fish":
		return filepath.Join(home, ".config", "fish", "completions", "hauntty.fish")
	}
	return ""
}
