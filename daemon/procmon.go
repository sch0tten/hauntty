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
	ClassRunning      CommandClassification = "running"       // CPU or I/O active in the foreground group
	ClassWaitingInput CommandClassification = "waiting_input" // foreground sleeping on tty read
	ClassIOWait       CommandClassification = "io_wait"       // kernel disk I/O wait (state D)
	ClassIdle         CommandClassification = "idle"          // foreground sleeping, not on tty
	ClassZombie       CommandClassification = "zombie"        // shell exited / zombie
	ClassDone         CommandClassification = "done"          // shell is foreground at prompt, no command running
	ClassUnknown      CommandClassification = "unknown"       // can't determine
)

// ProcessState holds parsed /proc data for a single process.
type ProcessState struct {
	PID     int
	Pgrp    int    // process group id (field 5 of /proc/<pid>/stat)
	Tpgid   int    // controlling-terminal foreground pgrp (field 8); -1 if no tty
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
	Foreground     []ProcessState // processes in the terminal foreground group — the running command
	Background     []ProcessState // lingering background jobs (started with &) under this shell
	BashState      ProcessState
	CPUPct         float64       // estimated CPU% across the foreground group
	IOReadBytes    int64         // total I/O read across the foreground group
	IOWriteBytes   int64         // total I/O write across the foreground group
	Elapsed        time.Duration // since the monitor started
}

// BackgroundPIDs returns the PIDs of lingering background jobs.
func (s *CommandStatus) BackgroundPIDs() []int {
	pids := make([]int, 0, len(s.Background))
	for _, p := range s.Background {
		pids = append(pids, p.PID)
	}
	return pids
}

// ProcMonitor watches a bash process and its foreground process group via /proc.
//
// The key signal is the controlling terminal's foreground process group
// (tpgid). When a command runs in the foreground, tpgid points at that
// command's process group; background jobs (started with &) live in other
// groups and never become the foreground. This lets us monitor exactly the
// command bash is currently running — immune to background jobs and leftover
// children from previous commands, which previously corrupted classification.
type ProcMonitor struct {
	bashPID   int
	bashPgrp  int // cached on first successful sample
	startTime time.Time

	// Per-PID cumulative CPU jiffies from the previous sample, for delta calc.
	prevCPU      map[int]uint64
	prevSampleAt time.Time
}

// NewProcMonitor creates a monitor for the given bash PID.
func NewProcMonitor(bashPID int) *ProcMonitor {
	return &ProcMonitor{
		bashPID:   bashPID,
		startTime: time.Now(),
		prevCPU:   make(map[int]uint64),
	}
}

// Sample reads current process state and classifies the command.
func (pm *ProcMonitor) Sample() (*CommandStatus, error) {
	now := time.Now()

	bashState, err := readProcessState(pm.bashPID)
	if err != nil {
		return nil, fmt.Errorf("read bash state: %w", err)
	}

	// Cache bash's own process group the first time we can read it.
	if pm.bashPgrp == 0 && bashState.Pgrp > 0 {
		pm.bashPgrp = bashState.Pgrp
	}

	status := &CommandStatus{
		BashState: bashState,
		Elapsed:   now.Sub(pm.startTime),
	}

	// Shell has exited / is a zombie.
	if bashState.State == 'Z' || bashState.Tpgid == -1 {
		status.Classification = ClassZombie
		pm.prevCPU = map[int]uint64{}
		pm.prevSampleAt = now
		return status, nil
	}

	// Enumerate the shell's descendants once and partition by process group.
	descs := descendants(pm.bashPID)
	fgPgrp := bashState.Tpgid
	for _, pid := range descs {
		ps, err := readProcessState(pid)
		if err != nil {
			continue
		}
		switch {
		case ps.Pgrp == fgPgrp && fgPgrp != pm.bashPgrp:
			// A real foreground command (its own group, distinct from bash).
			status.Foreground = append(status.Foreground, ps)
		case ps.Pgrp != pm.bashPgrp:
			// Different group from both bash and (current) foreground → a
			// detached background job spawned with &.
			status.Background = append(status.Background, ps)
		}
	}

	// CPU% and I/O are attributed to the foreground group only.
	status.CPUPct = pm.cpuPercent(status.Foreground, now)
	for _, c := range status.Foreground {
		status.IOReadBytes += c.IORead
		status.IOWriteBytes += c.IOWrite
	}

	status.Classification = pm.classify(bashState, status.Foreground)
	pm.prevSampleAt = now
	return status, nil
}

// cpuPercent computes CPU% from per-PID jiffy deltas, avoiding the uint64
// underflow that happens when the set of foreground processes changes between
// samples (a previous design summed totals and subtracted, which wrapped).
func (pm *ProcMonitor) cpuPercent(fg []ProcessState, now time.Time) float64 {
	cur := make(map[int]uint64, len(fg))
	var jiffyDelta uint64
	for _, c := range fg {
		total := c.Utime + c.Stime
		cur[c.PID] = total
		if prev, ok := pm.prevCPU[c.PID]; ok && total >= prev {
			jiffyDelta += total - prev
		}
		// New PIDs contribute 0 this round (we have no prior baseline).
	}

	var pct float64
	if !pm.prevSampleAt.IsZero() {
		wall := now.Sub(pm.prevSampleAt).Seconds()
		if wall > 0 {
			pct = (float64(jiffyDelta) / 100.0 / wall) * 100.0 // 100 Hz jiffies
		}
	}
	pm.prevCPU = cur

	if pct < 0 {
		pct = 0
	}
	// Allow >100% for multi-core foreground groups, but cap defensively.
	if pct > 100*float64(len(fg)+1) {
		pct = 100 * float64(len(fg)+1)
	}
	return pct
}

// classify determines the command state from the bash state and the foreground
// process group.
func (pm *ProcMonitor) classify(bash ProcessState, fg []ProcessState) CommandClassification {
	if bash.State == 'Z' {
		return ClassZombie
	}

	// Bash itself owns the terminal foreground → no external command is running.
	if bash.Tpgid == pm.bashPgrp || bash.Tpgid <= 0 {
		// Sleeping on a tty read: either at the prompt waiting for the next
		// command (the rc-file poll decides "done"), or blocked in a `read`
		// builtin. The controller debounces and consults the rc file to tell
		// these apart, so report the tty-wait state here.
		if isTTYRead(bash.Wchan) {
			return ClassDone
		}
		return ClassIdle
	}

	// A foreground command is running in group bash.Tpgid.
	if len(fg) == 0 {
		// Brief window: the command just exited but bash hasn't reclaimed the
		// terminal yet. Treat as transitioning, not done.
		return ClassIdle
	}

	hasRunning, hasIOWait, hasTTYWait, hasZombie := false, false, false, false
	for _, c := range fg {
		switch c.State {
		case 'R':
			hasRunning = true
		case 'D':
			hasIOWait = true
		case 'Z':
			hasZombie = true
		case 'S', 'I':
			if isTTYRead(c.Wchan) {
				hasTTYWait = true
			}
		}
	}

	switch {
	case hasTTYWait:
		return ClassWaitingInput
	case hasRunning:
		return ClassRunning
	case hasIOWait:
		return ClassIOWait
	case hasZombie:
		return ClassZombie
	default:
		return ClassIdle
	}
}

// isTTYRead returns true if the wchan indicates the process is waiting for
// terminal input. wait_woken is the common modern wchan for a tty read.
func isTTYRead(wchan string) bool {
	switch wchan {
	case "n_tty_read", "tty_read", "wait_woken", "tty_wait_until_sent":
		return true
	}
	return false
}

// readProcessState reads /proc/<pid>/{stat,wchan,io} for a single process.
func readProcessState(pid int) (ProcessState, error) {
	ps := ProcessState{PID: pid, Tpgid: 0}

	statData, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return ps, err
	}
	if err := parseStat(string(statData), &ps); err != nil {
		return ps, err
	}

	if wchanData, err := os.ReadFile(fmt.Sprintf("/proc/%d/wchan", pid)); err == nil {
		ps.Wchan = strings.TrimSpace(string(wchanData))
		if ps.Wchan == "0" {
			ps.Wchan = ""
		}
	}

	if ioData, err := os.ReadFile(fmt.Sprintf("/proc/%d/io", pid)); err == nil {
		parseIO(string(ioData), &ps)
	}

	return ps, nil
}

// parseStat extracts fields from /proc/<pid>/stat.
// Layout after "(comm) ": state(0) ppid(1) pgrp(2) session(3) tty_nr(4)
// tpgid(5) flags(6) minflt(7) cminflt(8) majflt(9) cmajflt(10) utime(11)
// stime(12) ...
func parseStat(data string, ps *ProcessState) error {
	// comm can contain spaces and parens, so split on the LAST ')'.
	closeIdx := strings.LastIndex(data, ")")
	if closeIdx < 0 || closeIdx+2 > len(data) {
		return fmt.Errorf("malformed stat: no closing paren")
	}

	fields := strings.Fields(data[closeIdx+2:])
	if len(fields) < 13 {
		return fmt.Errorf("malformed stat: too few fields after comm")
	}

	if len(fields[0]) > 0 {
		ps.State = fields[0][0]
	}
	ps.Pgrp, _ = strconv.Atoi(fields[2])
	ps.Tpgid, _ = strconv.Atoi(fields[5])
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

// descendants returns all descendant PIDs of the given PID (children,
// grandchildren, ...). It prefers the cheap /proc/<pid>/task/<tid>/children
// interface and falls back to a single /proc scan that builds a ppid map.
func descendants(pid int) []int {
	seen := make(map[int]bool)
	var out []int

	// BFS via the children files (available on modern Linux: CONFIG_PROC_CHILDREN).
	if walkChildrenFiles(pid, seen, &out) {
		return out
	}

	// Fallback: one pass over /proc to build ppid -> children, then BFS.
	childrenOf := buildPPIDMap()
	queue := append([]int(nil), childrenOf[pid]...)
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
		queue = append(queue, childrenOf[c]...)
	}
	return out
}

// walkChildrenFiles does a BFS using /proc/<pid>/task/<tid>/children. Returns
// false if the children interface is unavailable (so the caller can fall back).
func walkChildrenFiles(root int, seen map[int]bool, out *[]int) bool {
	available := false
	queue := []int{root}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]

		taskDir := fmt.Sprintf("/proc/%d/task", pid)
		tasks, err := os.ReadDir(taskDir)
		if err != nil {
			continue
		}
		for _, task := range tasks {
			data, err := os.ReadFile(fmt.Sprintf("%s/%s/children", taskDir, task.Name()))
			if err != nil {
				continue
			}
			available = true
			for _, f := range strings.Fields(string(data)) {
				if c, err := strconv.Atoi(f); err == nil && !seen[c] {
					seen[c] = true
					*out = append(*out, c)
					queue = append(queue, c)
				}
			}
		}
	}
	return available
}

// buildPPIDMap scans /proc once and returns a map of ppid -> child pids.
func buildPPIDMap() map[int][]int {
	m := make(map[int][]int)
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return m
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
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
		if ppid, err := strconv.Atoi(fields[1]); err == nil {
			m[ppid] = append(m[ppid], pid)
		}
	}
	return m
}
