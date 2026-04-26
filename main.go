package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"sort"
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
	if err := opencode.EnsureLocalServer(); err != nil {
		die("%v", err)
	}
	serverURL := opencode.LocalServerURL()

	if len(args) == 0 {
		// Create new local worktree
		repo, err := git.RepoRoot("")
		if err != nil {
			die("not in a git repo")
		}
		name := worktree.GenerateName()
		wtDir := repo + "/.worktrees/" + name
		if err := git.Pull("", repo); err != nil {
			die("failed to pull: %v", err)
		}
		if err := git.WorktreeAdd("", repo, name); err != nil {
			die("failed to create worktree: %v", err)
		}
		fmt.Fprintf(os.Stderr, "created worktree %s\n", name)
		if err := attach(serverURL, wtDir, ""); err != nil {
			die("%v", err)
		}
		printExitRow(serverURL, worktree.Entry{
			Name:      name,
			Dir:       wtDir,
			Repo:      repo,
			CreatedAt: time.Now(),
		})
		return
	}

	// Attach by name — search local and remote
	name := args[0]
	entry, ok := findWorktree(name)
	if !ok {
		die("worktree %q not found", name)
	}

	// Pull the repo's default branch to keep it fresh for new worktrees
	// and merge detection. Best-effort — warn and continue on failure.
	host := hostFor(entry)
	if err := git.Pull(host, entry.Repo); err != nil {
		fmt.Fprintf(os.Stderr, "warning: pull failed: %v\n", err)
	}

	if !entry.Remote {
		sessionID := opencode.FindLatestSession(serverURL, entry.Dir)
		if err := attach(serverURL, entry.Dir, sessionID); err != nil {
			die("%v", err)
		}
		printExitRow(serverURL, entry)
	} else {
		host, err := ssh.Host()
		if err != nil {
			die("%v", err)
		}
		if err := ssh.EnsureTunnel(host, opencode.TunnelPort(), opencode.ServerPort()); err != nil {
			die("%v", err)
		}
		if err := opencode.EnsureRemoteServer(host); err != nil {
			die("%v", err)
		}
		remoteURL := opencode.RemoteServerURL()
		sessionID := opencode.FindLatestSession(remoteURL, entry.Dir)
		if err := attach(remoteURL, entry.Dir, sessionID); err != nil {
			die("%v", err)
		}
		printExitRow(remoteURL, entry)
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
	if err := git.Pull(host, repo); err != nil {
		die("failed to pull: %v", err)
	}
	if err := git.WorktreeAdd(host, repo, name); err != nil {
		die("failed to create remote worktree: %v", err)
	}
	fmt.Fprintf(os.Stderr, "created worktree %s on %s\n", name, host)
	if err := ssh.EnsureTunnel(host, opencode.TunnelPort(), opencode.ServerPort()); err != nil {
		die("%v", err)
	}
	if err := opencode.EnsureRemoteServer(host); err != nil {
		die("%v", err)
	}
	serverURL := opencode.RemoteServerURL()
	sessionID := opencode.FindLatestSession(serverURL, wtDir)
	if err := attach(serverURL, wtDir, sessionID); err != nil {
		die("%v", err)
	}
	printExitRow(serverURL, worktree.Entry{
		Name:      name,
		Dir:       wtDir,
		Repo:      repo,
		Remote:    true,
		CreatedAt: time.Now(),
	})
}

type remoteResult struct {
	entries []worktree.Entry
	err     error
}

// findWorktree discovers all worktrees (local and remote) and returns the one matching name.
func findWorktree(name string) (worktree.Entry, bool) {
	host := os.Getenv("WT_REMOTE_HOST")

	localCh := make(chan []worktree.Entry, 1)
	remoteCh := make(chan remoteResult, 1)

	go func() { localCh <- discover.ListLocal() }()
	if host != "" {
		go func() {
			entries, err := discover.ListRemote(host)
			remoteCh <- remoteResult{entries, err}
		}()
	} else {
		remoteCh <- remoteResult{}
	}

	local := <-localCh
	rr := <-remoteCh
	if rr.err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", rr.err)
	}

	all := append(local, rr.entries...)
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
	if enrichErr != nil {
		die("%v", enrichErr)
	}
	worktree.Sort(all)

	if len(all) == 0 {
		fmt.Println("No worktrees found.")
		return
	}
	rows := make([]display.Row, len(all))
	for i, e := range all {
		rows[i] = display.Row{
			Entry:  e,
			Status: classifyStatus(e),
		}
	}
	display.PrintTable(rows)
}

// cmdRm handles: wt rm [name]
func cmdRm(args []string, remoteOnly bool) {
	if len(args) > 1 {
		die("unexpected argument: %s", args[1])
	}
	if len(args) == 1 {
		cmdRmTargeted(args[0])
	} else {
		cmdRmBatch(remoteOnly)
	}
}

// discoverAll discovers worktrees and enriches them with session data.
// Returns any enrichment error — callers that make safety decisions must
// check this; callers that only display can ignore it.
func discoverAll(remoteOnly bool) ([]worktree.Entry, error) {
	host := os.Getenv("WT_REMOTE_HOST")

	localCh := make(chan []worktree.Entry, 1)
	remoteCh := make(chan remoteResult, 1)

	if !remoteOnly {
		go func() { localCh <- discover.ListLocal() }()
	} else {
		localCh <- nil
	}

	if host != "" {
		go func() {
			entries, err := discover.ListRemote(host)
			remoteCh <- remoteResult{entries, err}
		}()
	} else {
		if remoteOnly {
			die("WT_REMOTE_HOST is not set\n\nRemote operations require an SSH host. Set the environment variable:\n\n  export WT_REMOTE_HOST=your-dev-desktop")
		}
		remoteCh <- remoteResult{}
	}

	// Discover in parallel, then fetch and enrich.
	local := <-localCh
	rr := <-remoteCh
	if rr.err != nil {
		if remoteOnly {
			die("%v", rr.err)
		}
		fmt.Fprintf(os.Stderr, "warning: %v\n", rr.err)
	}

	all := append(local, rr.entries...)
	fetchRepos(all)

	// Enrich using sub-slices of all so in-place mutations are visible
	// in the returned slice.
	localEntries := all[:len(local)]
	remoteEntries := all[len(local):]

	var enrichErr error
	if !remoteOnly {
		if err := opencode.EnsureLocalServer(); err != nil {
			enrichErr = fmt.Errorf("local server: %w", err)
		} else if err := opencode.Enrich(opencode.LocalServerURL(), localEntries); err != nil {
			enrichErr = fmt.Errorf("local session query: %w", err)
		}
	}
	if host != "" && rr.err == nil {
		if err := ssh.EnsureTunnel(host, opencode.TunnelPort(), opencode.ServerPort()); err != nil {
			enrichErr = fmt.Errorf("SSH tunnel: %w", err)
		} else if err := opencode.EnsureRemoteServer(host); err != nil {
			enrichErr = fmt.Errorf("remote server: %w", err)
		} else if err := opencode.Enrich(opencode.RemoteServerURL(), remoteEntries); err != nil {
			enrichErr = fmt.Errorf("remote session query: %w", err)
		}
	}

	return all, enrichErr
}

// hostFor returns the SSH host for an entry, or "" for local entries.
func hostFor(e worktree.Entry) string {
	if e.Remote {
		return os.Getenv("WT_REMOTE_HOST")
	}
	return ""
}

// fetchRepos runs git fetch once per unique repo to ensure remote-tracking
// refs are current. Best-effort — fetch failures are ignored.
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

// classifyStatus returns the single highest-priority status for a worktree.
// Priority: attached > working > dirty > merged > committed > idle > stale > empty.
func classifyStatus(e worktree.Entry) string {
	// Session states — active use takes priority
	if e.Attached {
		return "attached"
	}
	if e.Status == "working" {
		return "working"
	}

	// Git states — data safety
	host := hostFor(e)
	if !git.IsClean(host, e.Dir) {
		return "dirty"
	}
	unique := git.UniqueCommitCount(host, e.Repo, e.Name)
	if unique > 0 {
		if git.IsMerged(host, e.Repo, e.Name) {
			return "merged"
		}
		return "committed"
	}

	// Session lifecycle — no unique commits, clean tree
	if e.SessionID == "" {
		return "empty"
	}
	if !e.UpdatedAt.IsZero() && time.Since(e.UpdatedAt) > opencode.StaleThreshold {
		return "stale"
	}
	return "idle"
}

// isRemovable returns true if a status indicates the worktree is safe to remove.
func isRemovable(status string) bool {
	return status == "merged" || status == "stale" || status == "empty"
}

func cmdRmBatch(remoteOnly bool) {
	all, enrichErr := discoverAll(remoteOnly)
	if enrichErr != nil {
		die("cannot determine session status: %v", enrichErr)
	}

	if len(all) == 0 {
		fmt.Println("No worktrees found.")
		return
	}

	type result struct {
		entry  worktree.Entry
		status string
		errMsg string
	}
	var results []result
	var removeCount int

	for _, e := range all {
		status := classifyStatus(e)
		var errMsg string

		if isRemovable(status) {
			host := hostFor(e)
			if err := git.WorktreeRemove(host, e.Repo, e.Name); err != nil {
				errMsg = strings.ReplaceAll(strings.TrimSpace(err.Error()), "\n", " ")
			} else {
				status = "removed"
				removeCount++
			}
		}

		results = append(results, result{e, status, errMsg})
	}

	// Sort: removed first, then by activity (newest first)
	sort.SliceStable(results, func(i, j int) bool {
		ri, rj := results[i].status == "removed", results[j].status == "removed"
		if ri != rj {
			return ri
		}
		ti, tj := results[i].entry.UpdatedAt, results[j].entry.UpdatedAt
		if !ti.IsZero() && !tj.IsZero() {
			return ti.After(tj)
		}
		if !ti.IsZero() {
			return true
		}
		return !tj.IsZero() && false
	})

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

func cmdRmTargeted(name string) {
	entry, ok := findWorktree(name)
	if !ok {
		die("worktree %q not found", name)
	}
	host := hostFor(entry)
	if err := git.WorktreeForceRemove(host, entry.Repo, entry.Name); err != nil {
		die("%v", err)
	}
	display.PrintTable([]display.Row{{
		Entry:  entry,
		Status: "removed",
	}})
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
  wt rm                     Remove worktrees marked * in wt ls
  wt rm <name>              Remove a specific worktree

Status:
  attached    TUI client connected
  working     Agent generating
  dirty       Uncommitted changes in working tree
  merged *    Changes incorporated into default branch
  committed   Unique commits not yet in default branch
  idle        Session exists, no unique commits
  stale *     Session inactive >12 hours, no unique commits
  empty *     No session was ever created

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

// printExitRow queries the server for the session in dir and prints a single
// table row showing the worktree's current state. Best-effort; silently skipped
// if the server is unreachable.
func printExitRow(serverURL string, entry worktree.Entry) {
	entries := []worktree.Entry{entry}
	opencode.Enrich(serverURL, entries)
	entry = entries[0]
	display.PrintTable([]display.Row{{
		Entry:  entry,
		Status: classifyStatus(entry),
	}})
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
