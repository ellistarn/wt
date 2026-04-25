package opencode

import (
	"os/exec"
	"strings"
)

// AttachedDirs returns the set of worktree directories that have an
// opencode TUI client attached, detected by scanning local
// "opencode attach --dir <path>" processes.
func AttachedDirs() map[string]bool {
	out, err := exec.Command("ps", "-eo", "args").Output()
	if err != nil {
		return map[string]bool{}
	}

	result := make(map[string]bool)
	for _, line := range strings.Split(string(out), "\n") {
		if dir := parseAttachDir(line); dir != "" {
			result[dir] = true
		}
	}
	return result
}

// parseAttachDir extracts the --dir value from an "opencode attach" process
// command line. Returns empty string if the line is not an attach process.
func parseAttachDir(line string) string {
	if !strings.Contains(line, "opencode") || !strings.Contains(line, "attach") {
		return ""
	}
	fields := strings.Fields(line)
	for i, f := range fields {
		if f == "--dir" && i+1 < len(fields) {
			return fields[i+1]
		}
	}
	return ""
}
