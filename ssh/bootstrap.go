package ssh

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// BootstrapResult contains the result of a successful bootstrap.
type BootstrapResult struct {
	SID      string
	SockPath string // local socket path (forwarded)
	Host     string
}

// Bootstrap connects to a remote host, deploys hauntty if needed, starts the daemon,
// and sets up unix socket forwarding. Uses the user's SSH config/keys via ssh/scp commands.
func Bootstrap(host string, localBinary string) (*BootstrapResult, error) {
	if localBinary == "" {
		var err error
		localBinary, err = os.Executable()
		if err != nil {
			return nil, fmt.Errorf("find local binary: %w", err)
		}
	}

	remoteArch, err := detectRemoteArch(host)
	if err != nil {
		return nil, fmt.Errorf("detect remote arch: %w", err)
	}

	// Check if we need a cross-compiled binary
	remoteBin := localBinary
	if needsCrossBuild(remoteArch) {
		remoteBin, err = findCrossBinary(localBinary, remoteArch)
		if err != nil {
			return nil, fmt.Errorf("no binary for remote arch %s: %w", remoteArch, err)
		}
	}

	remotePath := "/tmp/hauntty"

	// Check remote version
	needsDeploy, err := checkNeedsDeploy(host, remotePath, localBinary)
	if err != nil {
		// Binary likely doesn't exist — deploy it
		needsDeploy = true
	}

	if needsDeploy {
		fmt.Fprintf(os.Stderr, "deploying hauntty to %s...\n", host)
		if err := deploy(host, remoteBin, remotePath); err != nil {
			return nil, fmt.Errorf("deploy: %w", err)
		}
	}

	// Check for existing daemon
	sid, err := findExistingSession(host, remotePath)
	if err == nil && sid != "" {
		fmt.Fprintf(os.Stderr, "found existing session: %s\n", sid)
		// Set up socket forwarding for existing session
		localSock, err := forwardSocket(host, sid)
		if err != nil {
			return nil, fmt.Errorf("forward socket: %w", err)
		}
		return &BootstrapResult{SID: sid, SockPath: localSock, Host: host}, nil
	}

	// Start daemon remotely
	fmt.Fprintf(os.Stderr, "starting hauntty daemon on %s...\n", host)
	sid, err = startRemoteDaemon(host, remotePath)
	if err != nil {
		return nil, fmt.Errorf("start daemon: %w", err)
	}

	fmt.Fprintf(os.Stderr, "session: %s\n", sid)

	// Set up socket forwarding
	localSock, err := forwardSocket(host, sid)
	if err != nil {
		return nil, fmt.Errorf("forward socket: %w", err)
	}

	return &BootstrapResult{SID: sid, SockPath: localSock, Host: host}, nil
}

// detectRemoteArch returns the remote host's architecture (amd64/arm64).
func detectRemoteArch(host string) (string, error) {
	out, err := sshRun(host, "uname -m")
	if err != nil {
		return "", err
	}
	arch := strings.TrimSpace(out)
	switch arch {
	case "x86_64":
		return "amd64", nil
	case "aarch64":
		return "arm64", nil
	default:
		return arch, nil
	}
}

func needsCrossBuild(remoteArch string) bool {
	return remoteArch != runtime.GOARCH
}

// findCrossBinary looks for a pre-built binary matching the remote architecture.
func findCrossBinary(localBinary string, remoteArch string) (string, error) {
	dir := filepath.Dir(localBinary)
	candidates := []string{
		filepath.Join(dir, fmt.Sprintf("hauntty-linux-%s", remoteArch)),
		filepath.Join(dir, fmt.Sprintf("hauntty_%s", remoteArch)),
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	return "", fmt.Errorf("no binary found for linux/%s (looked in %s)", remoteArch, dir)
}

func checkNeedsDeploy(host, remotePath, localBinary string) (bool, error) {
	// Check if remote binary exists and get its version
	remoteVersion, err := sshRun(host, remotePath+" version 2>/dev/null")
	if err != nil {
		return true, err
	}

	// Get local version
	out, err := exec.Command(localBinary, "version").Output()
	if err != nil {
		return true, nil
	}

	// Compare the full version strings (includes commit hash when stamped)
	remote := strings.TrimSpace(remoteVersion)
	local := strings.TrimSpace(string(out))

	// Both "dev" with no commit info → can't tell, force deploy
	if remote == "hauntty dev" && local == "hauntty dev" {
		fmt.Fprintf(os.Stderr, "both versions are 'dev' (no version stamp), forcing deploy\n")
		return true, nil
	}

	return remote != local, nil
}

func deploy(host, localBinary, remotePath string) error {
	cmd := exec.Command("scp", "-q", localBinary, fmt.Sprintf("%s:%s", host, remotePath))
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("scp: %w", err)
	}

	// Make executable
	_, err := sshRun(host, fmt.Sprintf("chmod +x %s", remotePath))
	return err
}

func findExistingSession(host, remotePath string) (string, error) {
	out, err := sshRun(host, remotePath+" list 2>/dev/null || true")
	if err != nil {
		return "", err
	}
	// Parse first live session ID from list output
	// Skip stale/error entries
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for _, line := range lines {
		if strings.Contains(line, "(stale)") || strings.Contains(line, "(error") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) > 1 && fields[0] != "no" {
			// Must have at least SID + pid= to be a live session
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("no sessions")
}

func startRemoteDaemon(host, remotePath string) (string, error) {
	// Start daemon in foreground mode via nohup, backgrounded with &
	// The daemon prints HAUNTTY_SID=... to stdout before entering the accept loop
	// We use a timeout read to capture those first lines
	// Use ~/.hauntty as base dir (user-writable, no root needed)
	startCmd := fmt.Sprintf(
		"nohup %s daemon --foreground --base-dir ~/.hauntty > /tmp/hauntty-boot.out 2>&1 & sleep 1 && head -5 /tmp/hauntty-boot.out",
		remotePath,
	)
	out, err := sshRun(host, startCmd)
	if err != nil {
		if out != "" {
			return parseSID(out)
		}
		return "", fmt.Errorf("start daemon: %w (output: %s)", err, out)
	}
	return parseSID(out)
}

func parseSID(output string) (string, error) {
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "HAUNTTY_SID=") {
			return strings.TrimPrefix(line, "HAUNTTY_SID="), nil
		}
	}
	return "", fmt.Errorf("could not parse SID from daemon output: %s", output)
}

// forwardSocket sets up SSH LocalForward for the unix socket.
// Returns the local socket path.
func forwardSocket(host, sid string) (string, error) {
	localSock := fmt.Sprintf("/tmp/hauntty-%s.sock", sid)
	remoteSock := fmt.Sprintf("/tmp/hauntty-%s.sock", sid)

	// Remove stale local socket
	os.Remove(localSock)

	// Start SSH tunnel in background with socket forwarding and keepalive
	cmd := exec.Command("ssh",
		"-N",                                         // don't execute remote command
		"-f",                                         // go to background
		"-o", "ExitOnForwardFailure=yes",             // fail if forwarding fails
		"-o", "StreamLocalBindUnlink=yes",            // remove stale local socket
		"-o", "ServerAliveInterval=60",               // send keepalive every 60s
		"-o", "ServerAliveCountMax=3",                // die after 3 missed keepalives
		"-L", fmt.Sprintf("%s:%s", localSock, remoteSock), // forward unix socket
		host,
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("ssh tunnel: %w", err)
	}

	// Wait for the socket to appear
	for i := 0; i < 20; i++ {
		if _, err := os.Stat(localSock); err == nil {
			return localSock, nil
		}
		time.Sleep(100 * time.Millisecond)
	}

	return localSock, nil // return anyway — might work
}

// sshRun executes a command on the remote host via ssh and returns stdout.
func sshRun(host, command string) (string, error) {
	cmd := exec.Command("ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", host, command)
	var stdout strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	return stdout.String(), err
}

// sshRunInteractive runs an SSH command that might produce streaming output.
func sshRunInteractive(host, command string) (string, error) {
	cmd := exec.Command("ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", host, command)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return "", err
	}

	scanner := bufio.NewScanner(stdout)
	var lines []string
	done := make(chan struct{})

	go func() {
		for scanner.Scan() {
			lines = append(lines, scanner.Text())
		}
		close(done)
	}()

	// Wait with timeout
	timer := time.AfterFunc(5*time.Second, func() {
		cmd.Process.Kill()
	})
	<-done
	timer.Stop()
	cmd.Wait()

	return strings.Join(lines, "\n"), nil
}
