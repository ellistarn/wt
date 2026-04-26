package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

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
	if len(remaining) > 0 && remaining[0] == "diff" {
		cmdDiff(remaining[1:])
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
		if err := attach(serverURL, wtDir, ""); err != nil {
			die("%v", err)
		}
		if err := git.Pull("", repo); err != nil {
			fmt.Fprintf(os.Stderr, "warning: pull failed: %v\n", err)
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

	// Pull the repo's current branch to keep it fresh for new worktrees
	// and merge detection. Best-effort — warn and continue on failure.
	host := hostFor(entry)
	if err := git.Pull(host, entry.Repo); err != nil {
		fmt.Fprintf(os.Stderr, "warning: pull failed: %v\n", err)
	}

	if entry.Host == "" {
		sessionID := opencode.FindLatestSession(serverURL, entry.Dir)
		if err := attach(serverURL, entry.Dir, sessionID); err != nil {
			die("%v", err)
		}
		if err := git.Pull(host, entry.Repo); err != nil {
			fmt.Fprintf(os.Stderr, "warning: pull failed: %v\n", err)
		}
		printExitRow(serverURL, entry)
	} else {
		if err := ssh.EnsureTunnel(entry.Host, opencode.TunnelPort(), opencode.ServerPort()); err != nil {
			die("%v", err)
		}
		if err := opencode.EnsureRemoteServer(entry.Host); err != nil {
			die("%v", err)
		}
		remoteURL := opencode.RemoteServerURL()
		sessionID := opencode.FindLatestSession(remoteURL, entry.Dir)
		if err := attach(remoteURL, entry.Dir, sessionID); err != nil {
			die("%v", err)
		}
		if err := git.Pull(host, entry.Repo); err != nil {
			fmt.Fprintf(os.Stderr, "warning: pull failed: %v\n", err)
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
	if err := git.Pull(host, repo); err != nil {
		fmt.Fprintf(os.Stderr, "warning: pull failed: %v\n", err)
	}
	printExitRow(serverURL, worktree.Entry{
		Name:      name,
		Dir:       wtDir,
		Repo:      repo,
		Host:      host,
		CreatedAt: time.Now(),
	})
}

// cmdLs handles: wt ls
func cmdLs(remoteOnly bool) {
	all, pulled, enrichErr := discoverAll(remoteOnly, true)
	if enrichErr != nil {
		die("%v", enrichErr)
	}
	worktree.Sort(all)

	if len(all) == 0 {
		fmt.Println("No worktrees found.")
		return
	}

	statuses := classifyAll(all, pulled)

	rows := make([]display.Row, len(all))
	for i, e := range all {
		rows[i] = display.Row{
			Entry:  e,
			Status: statuses[i],
		}
	}
	display.PrintTable(rows)
}

// cmdDiff handles: wt diff <name>
func cmdDiff(args []string) {
	if len(args) == 0 {
		die("usage: wt diff <name>")
	}
	name := args[0]
	entry, ok := findWorktree(name)
	if !ok {
		die("worktree %q not found", name)
	}
	host := hostFor(entry)

	if err := git.Pull(host, entry.Repo); err != nil {
		fmt.Fprintf(os.Stderr, "warning: pull failed: %v\n", err)
	}

	stat, err := git.DiffStat(host, entry.Dir)
	if err != nil {
		die("diff: %v", err)
	}
	if stat == "" {
		fmt.Println("No changes on this branch.")
		return
	}
	fmt.Println(stat)

	isTTY := isTerminal()
	full, err := git.Diff(host, entry.Dir, isTTY)
	if err != nil {
		die("diff: %v", err)
	}
	if isTTY {
		page(full)
	} else {
		fmt.Print(full)
	}
}

// isTerminal reports whether stdout is connected to a terminal.
func isTerminal() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// page displays content through less -R, preserving ANSI color codes.
// Falls back to direct output if the pager is unavailable.
func page(content string) {
	cmd := exec.Command("less", "-R")
	cmd.Stdin = strings.NewReader(content)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Print(content)
	}
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
  wt diff <name>            Show changes on a worktree's branch
  wt rm                     Remove worktrees marked * in wt ls
  wt rm <name>              Remove a specific worktree

Status:
  attached    TUI client connected
  working     Agent generating
  dirty       Uncommitted changes in working tree
  merged *    Changes incorporated into upstream
  committed   Unique commits not yet in upstream
  idle        Session exists, no unique commits
  stale *     Session inactive >4 hours, no unique commits
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
