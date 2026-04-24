package opencode

import (
	"os/exec"
	"strings"
)

// AttachedDirs returns a map of worktree directories to the number of
// opencode TUI clients connected to them. Detection is based on scanning
// local "opencode attach --dir <path>" processes.
func AttachedDirs() map[string]int {
	out, err := exec.Command("ps", "-eo", "args").Output()
	if err != nil {
		return map[string]int{}
	}

	result := make(map[string]int)
	for _, line := range strings.Split(string(out), "\n") {
		dir := parseAttachDir(line)
		if dir != "" {
			result[dir]++
		}
	}

	// Each attach shows up twice in ps (node wrapper + binary).
	// Normalize to actual client count.
	for dir := range result {
		result[dir] = result[dir] / 2
		if result[dir] == 0 {
			result[dir] = 1
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
