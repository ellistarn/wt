package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ellistarn/wt/pkg/display"
	"github.com/ellistarn/wt/pkg/git"
	"github.com/ellistarn/wt/pkg/tmux"
	"github.com/ellistarn/wt/pkg/transport"
	"github.com/ellistarn/wt/pkg/worktree"
)

// cmdRm handles: wt rm [name]
func cmdRm(args []string) {
	if len(args) > 1 {
		die("unexpected argument: %s", args[1])
	}
	if len(args) == 1 {
		cmdRmTargeted(args[0])
	} else {
		cmdRmBatch()
	}
}

// remoteEntry pairs an index into the all slice with its worktree entry.
type remoteEntry struct {
	idx   int
	entry worktree.Entry
}

// classifyAll classifies all entries, batching remote entries into a single
// SSH call per host and classifying local entries in parallel goroutines.
func classifyAll(all []worktree.Entry, pulled pullResult) []string {
	statuses := make([]string, len(all))

	remoteByHost := make(map[string][]remoteEntry)
	var localIdxs []int
	for i, e := range all {
		if e.Host != "" {
			remoteByHost[e.Host] = append(remoteByHost[e.Host], remoteEntry{i, e})
		} else {
			localIdxs = append(localIdxs, i)
		}
	}

	var wg sync.WaitGroup

	for host, entries := range remoteByHost {
		wg.Add(1)
		go func(host string, entries []remoteEntry) {
			defer wg.Done()
			classifyRemoteEntries(host, entries, all, statuses, pulled)
		}(host, entries)
	}

	sem := make(chan struct{}, 8)
	for _, i := range localIdxs {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, entry worktree.Entry) {
			defer wg.Done()
			defer func() { <-sem }()
			pulled.Wait(entry)
			statuses[idx] = classifyStatus(entry)
		}(i, all[i])
	}

	wg.Wait()
	return statuses
}

func classifyRemoteEntries(host string, entries []remoteEntry, all []worktree.Entry, statuses []string, pulled pullResult) {
	seen := make(map[string]bool)
	for _, re := range entries {
		if !seen[re.entry.Repo] {
			seen[re.entry.Repo] = true
			pulled.Wait(re.entry)
		}
	}

	var batchEntries []git.ClassifyEntry
	var batchIdxs []int
	for _, re := range entries {
		e := re.entry
		if e.Attached {
			statuses[re.idx] = "attached"
			continue
		}
		if e.Status == "working" {
			statuses[re.idx] = "working"
			continue
		}
		batchEntries = append(batchEntries, git.ClassifyEntry{
			Dir:    e.Dir,
			Repo:   e.Repo,
			Branch: e.Branch,
		})
		batchIdxs = append(batchIdxs, re.idx)
	}

	results, err := git.ClassifyBatch(host, batchEntries)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
		return
	}
	for j, r := range results {
		idx := batchIdxs[j]
		statuses[idx] = classifyFromResult(all[idx], r)
	}
}

// classifyStatus returns the single highest-priority status for a worktree.
func classifyStatus(e worktree.Entry) string {
	if e.Attached {
		return "attached"
	}
	if e.Status == "working" {
		return "working"
	}

	host := e.Host
	hasDiff := git.HasDiff(host, e.Dir, e.Repo)

	if !hasDiff {
		if e.Branch == "" {
			if !e.HasSession() {
				return "empty"
			}
			if !e.UpdatedAt.IsZero() && time.Since(e.UpdatedAt) > worktree.StaleThreshold {
				return "stale"
			}
			return "idle"
		}
		unique := git.UniqueCommitCount(host, e.Repo, e.Branch)
		if unique > 0 && git.IsMerged(host, e.Repo, e.Branch) {
			return "merged"
		}
		if !e.HasSession() {
			return "empty"
		}
		if unique == 0 && git.IsBehindUpstream(host, e.Repo, e.Branch) {
			return "merged"
		}
		if !e.UpdatedAt.IsZero() && time.Since(e.UpdatedAt) > worktree.StaleThreshold {
			return "stale"
		}
		return "idle"
	}

	if e.Branch == "" {
		return "dirty"
	}
	unique := git.UniqueCommitCount(host, e.Repo, e.Branch)
	if unique == 0 {
		return "dirty"
	}
	if git.IsMerged(host, e.Repo, e.Branch) {
		if git.HasUncommittedChanges(host, e.Dir) {
			return "dirty"
		}
		return "merged"
	}
	return "committed"
}

func classifyFromResult(e worktree.Entry, r git.ClassifyResult) string {
	if !r.HasDiff {
		if e.Branch == "" {
			if !e.HasSession() {
				return "empty"
			}
			if !e.UpdatedAt.IsZero() && time.Since(e.UpdatedAt) > worktree.StaleThreshold {
				return "stale"
			}
			return "idle"
		}
		if r.Unique > 0 && r.Merged {
			return "merged"
		}
		if !e.HasSession() {
			return "empty"
		}
		if r.Unique == 0 && r.Behind {
			return "merged"
		}
		if !e.UpdatedAt.IsZero() && time.Since(e.UpdatedAt) > worktree.StaleThreshold {
			return "stale"
		}
		return "idle"
	}

	if e.Branch == "" {
		return "dirty"
	}
	if r.Unique == 0 {
		return "dirty"
	}
	if r.Merged {
		if r.HasUncommitted {
			return "dirty"
		}
		return "merged"
	}
	return "committed"
}

func isRemovable(status string) bool {
	return status == "merged" || status == "stale" || status == "empty"
}

type rmResult struct {
	entry  worktree.Entry
	status string
	errMsg string
}

func sortRmResults(results []rmResult) {
	sort.SliceStable(results, func(i, j int) bool {
		ri, rj := results[i].status == "removed", results[j].status == "removed"
		if ri != rj {
			return rj
		}
		ti, tj := results[i].entry.UpdatedAt, results[j].entry.UpdatedAt
		if !ti.IsZero() && !tj.IsZero() {
			return ti.After(tj)
		}
		if !ti.IsZero() {
			return true
		}
		return false
	})
}

func cmdRmBatch() {
	all, pulled := discoverAll(true)

	if len(all) == 0 {
		fmt.Println("No worktrees found.")
		return
	}

	statuses := classifyAll(all, pulled)

	var results []rmResult
	var removeCount int

	for i, e := range all {
		status := statuses[i]
		var errMsg string

		if isRemovable(status) {
			t := transportFor(e)
			tmux.KillSession(t, tmux.SessionName(e.Name))

			host := e.Host
			if err := git.WorktreeRemove(host, e.Repo, e.Branch, e.Dir); err != nil {
				errMsg = strings.ReplaceAll(strings.TrimSpace(err.Error()), "\n", " ")
			} else {
				status = "removed"
				removeCount++
			}
		}

		results = append(results, rmResult{e, status, errMsg})
	}

	// Kill orphaned tmux sessions (session exists but no matching worktree)
	removeCount += killOrphanSessions(all)

	sortRmResults(results)

	rows := make([]display.Row, len(results))
	for i, r := range results {
		rows[i] = display.Row{
			Entry:  r.entry,
			Status: r.status,
		}
	}
	display.PrintTable(rows)

	for _, r := range results {
		if r.errMsg != "" {
			fmt.Fprintf(os.Stderr, "ERROR: %s: %s\n", r.entry.Name, r.errMsg)
		}
	}

	if removeCount == 0 {
		fmt.Println()
		fmt.Println("Nothing to remove. Use 'wt rm <name>' to target specific worktrees.")
	}
}

// killOrphanSessions kills wt/ tmux sessions that don't correspond to any
// known worktree, on both local and remote.
func killOrphanSessions(worktrees []worktree.Entry) int {
	known := make(map[string]bool, len(worktrees))
	for _, e := range worktrees {
		known[tmux.SessionName(e.Name)] = true
	}

	killed := 0

	localT := transport.NewLocal()
	for _, s := range tmux.ListSessions(localT) {
		if !known[s] {
			tmux.KillSession(localT, s)
			killed++
		}
	}

	if host := os.Getenv("WT_REMOTE_HOST"); host != "" {
		remoteT := transport.NewSSH(host)
		for _, s := range tmux.ListSessions(remoteT) {
			if !known[s] {
				tmux.KillSession(remoteT, s)
				killed++
			}
		}
	}

	return killed
}

func cmdRmTargeted(name string) {
	entry, ok := findWorktree(name)
	if !ok {
		die("worktree %q not found", name)
	}

	t := transportFor(entry)
	tmux.KillSession(t, tmux.SessionName(entry.Name))

	host := entry.Host
	if err := git.WorktreeForceRemove(host, entry.Repo, entry.Branch, entry.Dir); err != nil {
		die("%v", err)
	}
	display.PrintTable([]display.Row{{
		Entry:  entry,
		Status: "removed",
	}})
}
