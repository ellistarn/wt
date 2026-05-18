package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ellistarn/wt/pkg/discover"
	"github.com/ellistarn/wt/pkg/git"
	"github.com/ellistarn/wt/pkg/provider"
	"github.com/ellistarn/wt/pkg/worktree"
)

// findWorktree discovers all local worktrees and returns the one matching name.
func findWorktree(name string) (worktree.Entry, bool) {
	all := discover.ListLocal()
	for _, e := range all {
		if e.Name == name {
			return e, true
		}
	}
	suffix := "-" + name
	for _, e := range all {
		if strings.HasSuffix(e.Name, suffix) {
			return e, true
		}
	}
	return worktree.Entry{}, false
}

// discoverAll discovers worktrees, enriches them with session data,
// and starts pulls for each unique repo.
func discoverAll(pull bool) ([]worktree.Entry, pullResult) {
	all := discover.ListLocal()
	enrichAll(all)

	var pulled pullResult
	if pull {
		pulled = startPullRepos(all)
	} else {
		pulled = make(pullResult)
	}
	return all, pulled
}

// enrichAll enriches worktree entries with agent session detection and metadata.
func enrichAll(entries []worktree.Entry) {
	if len(entries) == 0 {
		return
	}
	procs := findAgentProcesses()

	for i := range entries {
		dir := entries[i].Dir

		// Query provider for title and activity
		meta := worktree.ReadMetadata(entries[i].Repo, entries[i].Name)
		cmd := meta.Cmd
		if cmd == "" {
			cmd = os.Getenv("WT_CMD")
		}
		if cmd == "" {
			cmd = "opencode"
		}

		info := provider.Query(dir, cmd)

		if info.Title != "" {
			entries[i].Title = info.Title
		} else if meta.Title != "" {
			entries[i].Title = meta.Title
		}
		if !info.Activity.IsZero() {
			entries[i].UpdatedAt = info.Activity
		}
		if info.Tokens > 0 {
			entries[i].Tokens = info.Tokens
		}
		if info.SubTokens > 0 {
			entries[i].SubTokens = info.SubTokens
		}

		// Check if agent process is running
		if procs[dir] {
			entries[i].Status = "active"
		}
	}
}

// agentNames is the list of known agent process names to detect.
var agentNames = []string{"opencode", "claude"}

// findAgentProcesses returns a map of working directories where an agent is running.
func findAgentProcesses() map[string]bool {
	result := make(map[string]bool)

	out, err := exec.Command("ps", "-eo", "pid,comm").Output()
	if err != nil {
		return result
	}

	var pids []string
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			base := filepath.Base(fields[1])
			for _, name := range agentNames {
				if base == name {
					pids = append(pids, fields[0])
					break
				}
			}
		}
	}

	for _, pid := range pids {
		out, err := exec.Command("lsof", "-a", "-p", pid, "-d", "cwd", "-Fn").Output()
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(line, "n") {
				dir := strings.TrimPrefix(line, "n")
				result[dir] = true
			}
		}
	}

	return result
}

// pullResult holds per-repo done channels from a non-blocking pull.
type pullResult map[string]<-chan struct{}

func (f pullResult) Wait(e worktree.Entry) {
	if ch, ok := f[e.Repo]; ok {
		<-ch
	}
}

func startPullRepos(entries []worktree.Entry) pullResult {
	seen := make(map[string]bool)
	result := make(pullResult)
	for _, e := range entries {
		if seen[e.Repo] {
			continue
		}
		seen[e.Repo] = true
		ch := make(chan struct{})
		result[e.Repo] = ch
		go func(repo string, ch chan struct{}) {
			if err := git.Pull(repo); err != nil {
				fmt.Fprintf(os.Stderr, "warning: pull failed for %s: %v\n", repo, err)
			}
			close(ch)
		}(e.Repo, ch)
	}
	return result
}
