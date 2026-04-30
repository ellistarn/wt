package opencode

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// AttachedDirs returns the set of worktree directories that have an
// opencode TUI client attached, detected by scanning local
// "opencode attach --dir <path>" processes.
//
// Orphaned attach processes (ppid == 1, reparented to launchd/init after
// the parent wt process died) are killed and excluded from the result.
func AttachedDirs() map[string]bool {
	out, err := exec.Command("ps", "-eo", "pid,ppid,args").Output()
	if err != nil {
		return map[string]bool{}
	}

	result := make(map[string]bool)
	for _, line := range strings.Split(string(out), "\n") {
		pid, ppid, dir := parseAttachProcess(line)
		if dir == "" {
			continue
		}
		if ppid == 1 {
			// Orphan — parent wt process died; kill and skip.
			syscall.Kill(pid, syscall.SIGTERM)
			continue
		}
		result[dir] = true
	}
	return result
}

// parseAttachProcess extracts the pid, ppid, and --dir value from a
// "ps -eo pid,ppid,args" line for an "opencode attach" process.
// Returns (0, 0, "") if the line is not an attach process.
func parseAttachProcess(line string) (pid, ppid int, dir string) {
	if !strings.Contains(line, "opencode") || !strings.Contains(line, "attach") {
		return 0, 0, ""
	}
	fields := strings.Fields(line)
	if len(fields) < 3 {
		return 0, 0, ""
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, 0, ""
	}
	ppid, err = strconv.Atoi(fields[1])
	if err != nil {
		return 0, 0, ""
	}
	// fields[2:] is the command line (args)
	for i := 2; i < len(fields); i++ {
		if fields[i] == "--dir" && i+1 < len(fields) {
			return pid, ppid, fields[i+1]
		}
	}
	return 0, 0, ""
}
