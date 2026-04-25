package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ellistarn/wt/pkg/discover"
	"github.com/ellistarn/wt/pkg/display"
	"github.com/ellistarn/wt/pkg/git"
	"github.com/ellistarn/wt/pkg/opencode"
	"github.com/ellistarn/wt/pkg/ssh"
	"github.com/ellistarn/wt/pkg/worktree"
)

func main() {
	args := os.Args[1:]

	// Parse global flags
	remote := false
	var remaining []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-r", "--remote":
			remote = true
		case "-h", "--help":
			printUsage()
			os.Exit(0)
		default:
			remaining = append(remaining, args[i])
		}
	}

	// Dispatch
	if len(remaining) > 0 && remaining[0] == "ls" {
		cmdLs(remote)
		return
	}
	if len(remaining) > 0 && remaining[0] == "rm" {
		cmdRm(remaining[1:], remote)
		return
	}

	if remote {
		cmdRemote(remaining)
	} else {
		cmdLocal(remaining)
	}
}

// cmdLocal handles: wt [name]
func cmdLocal(args []string) {
	serverURL := opencode.LocalServerURL()
	if err := opencode.CheckHealth(serverURL); err != nil {
		die("%v", err)
	}

	if len(args) == 0 {
		// Create new local worktree
		repo, err := git.RepoRoot("")
		if err != nil {
			die("not in a git repo")
		}
		name := worktree.GenerateName()
		wtDir := repo + "/.worktrees/" + name
		if err := git.WorktreeAdd("", repo, name); err != nil {
			die("failed to create worktree: %v", err)
		}
		fmt.Printf("wt %s\n", name)
		if err := attach(serverURL, wtDir, ""); err != nil {
			die("%v", err)
		}
		return
	}

	// Attach by name — search local and remote
	name := args[0]
	entry, ok := findWorktree(name)
	if !ok {
		die("worktree %q not found", name)
	}
	if !entry.Remote {
		sessionID := opencode.FindLatestSession(serverURL, entry.Dir)
		if err := attach(serverURL, entry.Dir, sessionID); err != nil {
			die("%v", err)
		}
	} else {
		remoteURL := opencode.RemoteServerURL()
		sessionID := opencode.FindLatestSession(remoteURL, entry.Dir)
		if err := attach(remoteURL, entry.Dir, sessionID); err != nil {
			die("%v", err)
		}
	}
}

// cmdRemote handles: wt -r <path>
func cmdRemote(args []string) {
	if len(args) == 0 {
		die("remote mode requires a repo path: wt -r <path>")
	}

	host, err := ssh.Host()
	if err != nil {
		die("%v", err)
	}
	remoteHome, err := ssh.ResolveRemoteHome(host)
	if err != nil {
		die("%v", err)
	}
	remotePath, err := ssh.ToRemotePath(args[0], remoteHome)
	if err != nil {
		die("%v", err)
	}

	repo, err := git.RepoRoot(host, remotePath)
	if err != nil {
		die("not a git repo on remote: %s", remotePath)
	}

	// Create new worktree
	name := worktree.GenerateName()
	wtDir := repo + "/.worktrees/" + name
	if err := git.WorktreeAdd(host, repo, name); err != nil {
		die("failed to create remote worktree: %v", err)
	}
	fmt.Printf("wt %s\n", name)

	serverURL := opencode.RemoteServerURL()
	sessionID := opencode.FindLatestSession(serverURL, wtDir)
	if err := attach(serverURL, wtDir, sessionID); err != nil {
		die("%v", err)
	}
}

// findWorktree discovers all worktrees (local and remote) and returns the one matching name.
func findWorktree(name string) (worktree.Entry, bool) {
	host := os.Getenv("DEV_DESKTOP_HOST")

	localCh := make(chan []worktree.Entry, 1)
	remoteCh := make(chan []worktree.Entry, 1)

	go func() { localCh <- discover.ListLocal() }()
	if host != "" {
		go func() { remoteCh <- discover.ListRemote(host) }()
	} else {
		remoteCh <- nil
	}

	all := append(<-localCh, <-remoteCh...)
	for _, e := range all {
		if e.Name == name {
			return e, true
		}
	}
	return worktree.Entry{}, false
}

// cmdLs handles: wt ls
func cmdLs(remoteOnly bool) {
	all, enrichErr := discoverAll(remoteOnly)
	worktree.Sort(all)

	if len(all) == 0 {
		fmt.Println("No worktrees found.")
		return
	}
	if enrichErr != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n\n", enrichErr)
	}
	display.PrintTable(all)
}

// cmdRm handles: wt rm [name] [--force] [--stale N] [--dry-run]
func cmdRm(args []string, remoteOnly bool) {
	var name string
	force := false
	dryRun := false
	staleHours := 12

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--force":
			force = true
		case "--dry-run":
			dryRun = true
		case "--stale":
			if i+1 >= len(args) {
				die("--stale requires a number of hours")
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil {
				die("--stale requires a number of hours")
			}
			staleHours = n
			i++
		default:
			if name != "" {
				die("unexpected argument: %s", args[i])
			}
			name = args[i]
		}
	}

	if name != "" {
		cmdRmTargeted(name, remoteOnly, force, dryRun)
	} else {
		cmdRmBatch(remoteOnly, time.Duration(staleHours)*time.Hour, dryRun)
	}
}

// discoverAll discovers worktrees and enriches them with session data.
// Returns any enrichment error — callers that make safety decisions must
// check this; callers that only display can ignore it.
func discoverAll(remoteOnly bool) ([]worktree.Entry, error) {
	host := os.Getenv("DEV_DESKTOP_HOST")

	localCh := make(chan []worktree.Entry, 1)
	remoteCh := make(chan []worktree.Entry, 1)

	if !remoteOnly {
		go func() { localCh <- discover.ListLocal() }()
	} else {
		localCh <- nil
	}

	if host != "" {
		go func() { remoteCh <- discover.ListRemote(host) }()
	} else {
		if remoteOnly {
			die("DEV_DESKTOP_HOST is not set")
		}
		remoteCh <- nil
	}

	// Discover in parallel, then enrich sequentially.
	var local, remote []worktree.Entry

	local = <-localCh
	remote = <-remoteCh

	var enrichErr error
	if err := opencode.EnrichLocal(local); err != nil {
		enrichErr = fmt.Errorf("local session query: %w", err)
	}
	if host != "" {
		if err := opencode.EnrichRemote(host, remote); err != nil {
			enrichErr = fmt.Errorf("remote session query: %w", err)
		}
	}

	return append(local, remote...), enrichErr
}

// hostFor returns the SSH host for an entry, or "" for local entries.
func hostFor(e worktree.Entry) string {
	if e.Remote {
		return os.Getenv("DEV_DESKTOP_HOST")
	}
	return ""
}

// fetchRepos runs git fetch once per unique repo to ensure remote refs are
// current for safety checks. Best-effort — fetch failures are ignored.
func fetchRepos(entries []worktree.Entry) {
	type key struct{ host, repo string }
	seen := make(map[key]bool)
	for _, e := range entries {
		k := key{hostFor(e), e.Repo}
		if !seen[k] {
			seen[k] = true
			git.Fetch(k.host, k.repo)
		}
	}
}

// checkDataSafety checks the three data safety gates. Returns a list of reasons
// the worktree is NOT safe to remove. Empty list means data-safe.
func checkDataSafety(e worktree.Entry) []string {
	host := hostFor(e)
	var reasons []string

	if e.Status == "working" {
		reasons = append(reasons, "agent is working")
	}
	if !git.IsClean(host, e.Dir) {
		reasons = append(reasons, "uncommitted changes")
	}
	unique := git.UniqueCommitCount(host, e.Repo, e.Name)
	if unique > 0 && !git.IsPushed(host, e.Repo, e.Name) {
		reasons = append(reasons, fmt.Sprintf("%d unpushed commit(s)", unique))
	}

	return reasons
}

// checkWorkflowDone checks whether there's no reason to come back to this
// worktree. Returns true if at least one workflow gate passes.
func checkWorkflowDone(e worktree.Entry, staleThreshold time.Duration) bool {
	host := hostFor(e)

	// No session — worktree was never used
	if e.SessionID == "" {
		return true
	}

	// Branch merged — work has landed
	if git.IsMerged(host, e.Repo, e.Name) {
		return true
	}

	// Session stale + no unique commits — started, never committed, walked away
	unique := git.UniqueCommitCount(host, e.Repo, e.Name)
	if unique == 0 && !e.UpdatedAt.IsZero() && time.Since(e.UpdatedAt) > staleThreshold {
		return true
	}

	return false
}

// workflowSkipReason returns a human-readable reason why the worktree is not
// considered done. Only called when checkWorkflowDone returns false.
func workflowSkipReason(e worktree.Entry) string {
	host := hostFor(e)
	unique := git.UniqueCommitCount(host, e.Repo, e.Name)
	if unique > 0 {
		return "branch not merged"
	}
	return "session active"
}

func cmdRmBatch(remoteOnly bool, staleThreshold time.Duration, dryRun bool) {
	all, enrichErr := discoverAll(remoteOnly)
	if enrichErr != nil {
		die("cannot determine session status: %v", enrichErr)
	}
	fetchRepos(all)

	type result struct {
		entry  worktree.Entry
		reason string // empty = removed
	}
	var removed, skipped []result

	for _, e := range all {
		// Data safety gates
		if reasons := checkDataSafety(e); len(reasons) > 0 {
			skipped = append(skipped, result{e, strings.Join(reasons, ", ")})
			continue
		}
		// Workflow gates
		if !checkWorkflowDone(e, staleThreshold) {
			skipped = append(skipped, result{e, workflowSkipReason(e)})
			continue
		}
		// Safe to remove
		if !dryRun {
			host := hostFor(e)
			if err := git.WorktreeRemove(host, e.Repo, e.Name); err != nil {
				skipped = append(skipped, result{e, fmt.Sprintf("remove failed: %v", err)})
				continue
			}
		}
		removed = append(removed, result{e, ""})
	}

	now := time.Now()
	verb := "Removed"
	if dryRun {
		verb = "Would remove"
	}
	if len(removed) > 0 {
		fmt.Printf("%s:\n", verb)
		for _, r := range removed {
			fmt.Printf("  %s  (%s)\n", r.entry.Name, entryAge(r.entry, now))
		}
	}
	if len(skipped) > 0 {
		if len(removed) > 0 {
			fmt.Println()
		}
		fmt.Println("Skipped:")
		for _, r := range skipped {
			fmt.Printf("  %s  (%s) — %s\n", r.entry.Name, entryAge(r.entry, now), r.reason)
		}
	}
	if len(removed) == 0 && len(skipped) == 0 {
		fmt.Println("No worktrees found.")
	}
	if len(removed) == 0 && len(skipped) > 0 {
		fmt.Println()
		fmt.Println("Nothing to remove. Use 'wt rm <name>' to target specific worktrees.")
	}
}

func cmdRmTargeted(name string, remoteOnly bool, force bool, dryRun bool) {
	all, enrichErr := discoverAll(remoteOnly)

	var entry *worktree.Entry
	for i := range all {
		if all[i].Name == name {
			entry = &all[i]
			break
		}
	}
	if entry == nil {
		die("worktree %q not found", name)
	}

	host := hostFor(*entry)

	if force {
		if dryRun {
			fmt.Printf("Would remove %s (forced)\n", name)
			return
		}
		if err := git.WorktreeForceRemove(host, entry.Repo, entry.Name); err != nil {
			die("%v", err)
		}
		fmt.Printf("Removed %s (forced)\n", name)
		return
	}

	if enrichErr != nil {
		die("cannot determine session status: %v\n\nUse --force to override.", enrichErr)
	}

	// Fetch to ensure remote refs are current for safety checks
	git.Fetch(host, entry.Repo)

	// Data safety gates — hard block
	if reasons := checkDataSafety(*entry); len(reasons) > 0 {
		fmt.Fprintf(os.Stderr, "wt: cannot remove %s:\n", name)
		for _, r := range reasons {
			fmt.Fprintf(os.Stderr, "  - %s\n", r)
		}
		fmt.Fprintf(os.Stderr, "\nUse --force to override.\n")
		os.Exit(1)
	}

	// Workflow gates — warnings only
	if !checkWorkflowDone(*entry, 0) {
		fmt.Fprintf(os.Stderr, "warning: %s\n", workflowSkipReason(*entry))
	}

	if dryRun {
		fmt.Printf("Would remove %s\n", name)
		return
	}

	if err := git.WorktreeRemove(host, entry.Repo, entry.Name); err != nil {
		die("%v", err)
	}
	fmt.Printf("Removed %s\n", name)
}

// entryAge returns a human-readable age string for a worktree entry.
func entryAge(e worktree.Entry, now time.Time) string {
	t := e.UpdatedAt
	if t.IsZero() {
		t = e.CreatedAt
	}
	if t.IsZero() {
		return "unknown age"
	}
	return display.FormatAge(t, now)
}

func printUsage() {
	usage := strings.TrimSpace(`
wt — worktree session manager

Usage:
  wt                        Create a new local worktree and attach
  wt <name>                 Attach to an existing worktree (local or remote)
  wt -r <path>              Create a new remote worktree and attach
  wt ls                     List all worktrees (local and remote)
  wt -r ls                  List remote worktrees only
  wt rm                     Remove all safe-to-remove worktrees
  wt rm --dry-run           Preview what would be removed
  wt rm <name>              Remove a specific worktree (with safety checks)
  wt rm <name> --force      Remove a specific worktree (skip all checks)
  wt rm --stale 1           Remove safe worktrees (1-hour stale threshold)

Flags:
  -r, --remote              Operate on the remote dev desktop
  -h, --help                Show this help
`)
	fmt.Println(usage)
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "wt: "+format+"\n", args...)
	os.Exit(1)
}

// attach runs opencode attach as a subprocess, connecting to the given server.
func attach(serverURL, dir, sessionID string) error {
	binary, err := exec.LookPath("opencode")
	if err != nil {
		return fmt.Errorf("opencode not found in PATH")
	}
	args := []string{"attach", serverURL, "--dir", dir}
	if sessionID != "" {
		args = append(args, "--session", sessionID)
	}
	return runTUI(exec.Command(binary, args...))
}

// runTUI runs a TUI command as a subprocess, letting it own the terminal.
// Terminal signals are ignored in the parent so the child handles them.
// The TUI's alternate screen buffer handles cleanup automatically on exit.
func runTUI(cmd *exec.Cmd) error {
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Let the child handle all terminal signals; parent just waits.
	signal.Ignore(syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTSTP)

	err := cmd.Run()

	if err != nil {
		// Forward the child's exit code.
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		return err
	}
	return nil
}
