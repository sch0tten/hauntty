package daemon

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// CommandClassification describes the observed state of a running command.
type CommandClassification string

const (
	ClassRunning      CommandClassification = "running"       // CPU or I/O active
	ClassWaitingInput CommandClassification = "waiting_input" // sleeping on tty read
	ClassIOWait       CommandClassification = "io_wait"       // kernel disk I/O wait (state D)
	ClassIdle         CommandClassification = "idle"          // sleeping, not on tty
	ClassZombie       CommandClassification = "zombie"        // zombie process
	ClassDone         CommandClassification = "done"          // no child found, bash at prompt
	ClassUnknown      CommandClassification = "unknown"       // can't determine
)

// ProcessState holds parsed /proc data for a single process.
type ProcessState struct {
	PID     int
	State   byte   // R, S, D, Z, T from /proc/<pid>/stat
	Wchan   string // kernel wait channel from /proc/<pid>/wchan
	Utime   uint64 // user CPU jiffies from /proc/<pid>/stat
	Stime   uint64 // system CPU jiffies from /proc/<pid>/stat
	IORead  int64  // bytes read from /proc/<pid>/io
	IOWrite int64  // bytes written from /proc/<pid>/io
}

// CommandStatus is the high-level status returned by Sample().
type CommandStatus struct {
	Classification CommandClassification
	Children       []ProcessState
	BashState      ProcessState
	CPUPct         float64 // estimated CPU% across all children
	IOReadBytes    int64   // total I/O read across children
	IOWriteBytes   int64   // total I/O write across children
	Elapsed        time.Duration
}

// ProcMonitor watches a bash process and its children via /proc.
type ProcMonitor struct {
	bashPID   int
	startTime time.Time

	// Previous sample for computing deltas
	prevChildren []ProcessState
	prevSampleAt time.Time
}

// NewProcMonitor creates a monitor for the given bash PID.
func NewProcMonitor(bashPID int) *ProcMonitor {
	return &ProcMonitor{
		bashPID:   bashPID,
		startTime: time.Now(),
	}
}

// Sample reads current process state and classifies the command.
func (pm *ProcMonitor) Sample() (*CommandStatus, error) {
	now := time.Now()

	// Read bash process state
	bashState, err := readProcessState(pm.bashPID)
	if err != nil {
		return nil, fmt.Errorf("read bash state: %w", err)
	}

	// Find child processes
	childPIDs, err := findChildren(pm.bashPID)
	if err != nil {
		childPIDs = nil // not fatal — command may have finished
	}

	// Read state for each child
	var children []ProcessState
	for _, pid := range childPIDs {
		if cs, err := readProcessState(pid); err == nil {
			children = append(children, cs)
			// Also check grandchildren (pipelines, subshells)
			if gcPIDs, err := findChildren(pid); err == nil {
				for _, gcPID := range gcPIDs {
					if gcs, err := readProcessState(gcPID); err == nil {
						children = append(children, gcs)
					}
				}
			}
		}
	}

	// Compute CPU delta
	var cpuPct float64
	if pm.prevChildren != nil && !pm.prevSampleAt.IsZero() {
		wallDelta := now.Sub(pm.prevSampleAt).Seconds()
		if wallDelta > 0 {
			var curTotal, prevTotal uint64
			for _, c := range children {
				curTotal += c.Utime + c.Stime
			}
			for _, c := range pm.prevChildren {
				prevTotal += c.Utime + c.Stime
			}
			// jiffies to seconds (typically 100 Hz)
			jiffyDelta := float64(curTotal-prevTotal) / 100.0
			cpuPct = (jiffyDelta / wallDelta) * 100.0
			if cpuPct > 100 {
				cpuPct = 100
			}
			if cpuPct < 0 {
				cpuPct = 0
			}
		}
	}

	// Compute total I/O
	var ioRead, ioWrite int64
	for _, c := range children {
		ioRead += c.IORead
		ioWrite += c.IOWrite
	}

	// Save for next delta
	pm.prevChildren = children
	pm.prevSampleAt = now

	// Classify
	classification := pm.classify(bashState, children)

	return &CommandStatus{
		Classification: classification,
		Children:       children,
		BashState:      bashState,
		CPUPct:         cpuPct,
		IOReadBytes:    ioRead,
		IOWriteBytes:   ioWrite,
		Elapsed:        now.Sub(pm.startTime),
	}, nil
}

// classify determines the command state from process data.
func (pm *ProcMonitor) classify(bash ProcessState, children []ProcessState) CommandClassification {
	// Bash itself is a zombie — shell exited (e.g., "exit N" command)
	if bash.State == 'Z' {
		return ClassZombie
	}

	// No children — command likely finished or is a shell builtin
	if len(children) == 0 {
		// Bash waiting for input = at prompt = command done
		if isTTYRead(bash.Wchan) {
			return ClassDone
		}
		// Bash waiting for child that already exited
		if bash.Wchan == "do_wait" || bash.Wchan == "wait_woken" {
			return ClassDone
		}
		return ClassIdle
	}

	// Check all children
	hasZombie := false
	hasRunning := false
	hasIOWait := false
	hasTTYWait := false

	for _, c := range children {
		switch c.State {
		case 'Z':
			hasZombie = true
		case 'D':
			hasIOWait = true
		case 'R':
			hasRunning = true
		case 'S', 'I':
			if isTTYRead(c.Wchan) {
				hasTTYWait = true
			}
		}
	}

	// Priority: waiting_input > running > io_wait > zombie > idle
	if hasTTYWait {
		return ClassWaitingInput
	}
	if hasRunning {
		return ClassRunning
	}
	if hasIOWait {
		return ClassIOWait
	}
	if hasZombie && !hasRunning {
		return ClassZombie
	}

	// All children sleeping but not on tty — could be pipe wait, network, etc.
	// Check if I/O is happening (from delta with previous sample)
	return ClassIdle
}

// isTTYRead returns true if the wchan indicates the process is waiting for terminal input.
func isTTYRead(wchan string) bool {
	switch wchan {
	case "n_tty_read", "tty_read", "wait_woken":
		return true
	}
	return false
}

// readProcessState reads /proc/<pid>/{stat,wchan,io} for a single process.
func readProcessState(pid int) (ProcessState, error) {
	ps := ProcessState{PID: pid}

	// Read /proc/<pid>/stat
	statData, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return ps, err
	}
	if err := parseStat(string(statData), &ps); err != nil {
		return ps, err
	}

	// Read /proc/<pid>/wchan (optional, may not exist)
	if wchanData, err := os.ReadFile(fmt.Sprintf("/proc/%d/wchan", pid)); err == nil {
		ps.Wchan = strings.TrimSpace(string(wchanData))
		if ps.Wchan == "0" {
			ps.Wchan = "" // 0 means not waiting
		}
	}

	// Read /proc/<pid>/io (optional, may need same UID)
	if ioData, err := os.ReadFile(fmt.Sprintf("/proc/%d/io", pid)); err == nil {
		parseIO(string(ioData), &ps)
	}

	return ps, nil
}

// parseStat extracts fields from /proc/<pid>/stat.
// Format: pid (comm) state ppid pgrp session tty_nr tpgid flags
//         minflt cminflt majflt cmajflt utime stime cutime cstime ...
func parseStat(data string, ps *ProcessState) error {
	// The comm field can contain spaces and parens, so find the last ')' first
	closeIdx := strings.LastIndex(data, ")")
	if closeIdx < 0 {
		return fmt.Errorf("malformed stat: no closing paren")
	}

	// Fields after ') '
	rest := data[closeIdx+2:]
	fields := strings.Fields(rest)
	if len(fields) < 13 {
		return fmt.Errorf("malformed stat: too few fields after comm")
	}

	// Field 0 = state (R, S, D, Z, T, etc.)
	if len(fields[0]) > 0 {
		ps.State = fields[0][0]
	}

	// Fields 11, 12 = utime, stime (0-indexed after comm: field indices 13, 14 in full line)
	ps.Utime, _ = strconv.ParseUint(fields[11], 10, 64)
	ps.Stime, _ = strconv.ParseUint(fields[12], 10, 64)

	return nil
}

// parseIO extracts read_bytes and write_bytes from /proc/<pid>/io.
func parseIO(data string, ps *ProcessState) {
	for _, line := range strings.Split(data, "\n") {
		if strings.HasPrefix(line, "read_bytes:") {
			ps.IORead, _ = strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "read_bytes:")), 10, 64)
		} else if strings.HasPrefix(line, "write_bytes:") {
			ps.IOWrite, _ = strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "write_bytes:")), 10, 64)
		}
	}
}

// findChildren returns PIDs of direct children of the given PID.
// Uses ps --ppid as fallback since /proc/<pid>/task/<tid>/children
// may not be available on all kernels.
func findChildren(pid int) ([]int, error) {
	// Try /proc first (faster, no subprocess)
	children, err := findChildrenProc(pid)
	if err == nil {
		return children, nil
	}

	// Fallback: read /proc/<pid>/task/<tid>/children
	return findChildrenTaskDir(pid)
}

// findChildrenProc scans /proc for processes whose ppid matches.
func findChildrenProc(ppid int) ([]int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}

	ppidStr := strconv.Itoa(ppid)
	var children []int

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue // not a PID directory
		}

		// Read /proc/<pid>/stat and check ppid (field 4, index 3 after comm)
		statData, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			continue
		}
		closeIdx := strings.LastIndex(string(statData), ")")
		if closeIdx < 0 {
			continue
		}
		fields := strings.Fields(string(statData)[closeIdx+2:])
		if len(fields) < 2 {
			continue
		}
		// Field 1 = ppid (0-indexed after comm)
		if fields[1] == ppidStr {
			children = append(children, pid)
		}
	}

	return children, nil
}

// findChildrenTaskDir tries /proc/<pid>/task/<tid>/children.
func findChildrenTaskDir(pid int) ([]int, error) {
	taskDir := fmt.Sprintf("/proc/%d/task", pid)
	tasks, err := os.ReadDir(taskDir)
	if err != nil {
		return nil, err
	}

	var children []int
	seen := make(map[int]bool)

	for _, task := range tasks {
		childFile := fmt.Sprintf("%s/%s/children", taskDir, task.Name())
		data, err := os.ReadFile(childFile)
		if err != nil {
			continue
		}
		for _, pidStr := range strings.Fields(string(data)) {
			if pid, err := strconv.Atoi(pidStr); err == nil && !seen[pid] {
				seen[pid] = true
				children = append(children, pid)
			}
		}
	}

	if len(children) == 0 && len(seen) == 0 {
		return nil, fmt.Errorf("no children file available")
	}
	return children, nil
}
