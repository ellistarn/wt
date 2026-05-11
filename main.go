package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ellistarn/wt/pkg/display"
	"github.com/ellistarn/wt/pkg/git"
	"github.com/ellistarn/wt/pkg/ssh"
	"github.com/ellistarn/wt/pkg/tmux"
	"github.com/ellistarn/wt/pkg/transport"
	"github.com/ellistarn/wt/pkg/worktree"
)

func main() {
	args := os.Args[1:]

	root := ""
	var remaining []string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--root":
			i++
			if i >= len(args) {
				die("--root requires a directory path")
			}
			root = args[i]
		case strings.HasPrefix(args[i], "--root="):
			root = strings.TrimPrefix(args[i], "--root=")
			if root == "" {
				die("--root requires a directory path")
			}
		case args[i] == "-h" || args[i] == "--help":
			printUsage()
			os.Exit(0)
		default:
			remaining = append(remaining, args[i])
		}
	}

	if len(remaining) == 0 {
		printUsage()
		os.Exit(0)
	}

	switch remaining[0] {
	case "ls":
		cmdLs()
	case "rm":
		cmdRm(remaining[1:])
	case "diff":
		cmdDiff(remaining[1:])
	default:
		cmdDispatch(remaining, root)
	}
}

// isPath returns true if the argument looks like a path (., ./, /, ~/, or host:).
func isPath(arg string) bool {
	if arg == "." || strings.HasPrefix(arg, "./") || strings.HasPrefix(arg, "/") || strings.HasPrefix(arg, "~/") {
		return true
	}
	if strings.Contains(arg, ":") {
		return true
	}
	return false
}

// parseTarget splits a path argument into host and path components.
// "host:path" → (host, path), "." → ("", resolved cwd repo), "/abs" → ("", "/abs")
func parseTarget(arg string) (host, path string) {
	if idx := strings.Index(arg, ":"); idx > 0 {
		return arg[:idx], arg[idx+1:]
	}
	return "", arg
}

func cmdDispatch(args []string, root string) {
	first := args[0]

	// If it's a path, create a new worktree there
	if isPath(first) {
		host, path := parseTarget(first)
		cmd := ""
		if len(args) > 1 {
			cmd = strings.Join(args[1:], " ")
		}
		if host != "" {
			cmdCreateRemote(host, path, root, cmd)
		} else {
			cmdCreateLocal(path, root, cmd)
		}
		return
	}

	// Try to resume an existing worktree
	if entry, ok := findWorktree(first); ok {
		cmdResume(entry)
		return
	}

	// Not a path and not a known worktree — treat as a remote host defaulting to ~
	cmd := ""
	if len(args) > 1 {
		cmd = strings.Join(args[1:], " ")
	}
	cmdCreateRemote(first, "~", root, cmd)
}

func cmdCreateLocal(path, root, cmd string) {
	t := transport.NewLocal()

	repo, err := git.RepoRoot("", path)
	if err != nil {
		die("not in a git repo: %s", path)
	}
	cmdCreate(t, repo, root, cmd)
}

func cmdCreateRemote(host, path, root, cmd string) {
	t := transport.NewSSH(host)

	remoteHome, err := ssh.ResolveRemoteHome(host)
	if err != nil {
		die("%v", err)
	}
	remotePath, err := ssh.ResolvePath(path, remoteHome)
	if err != nil {
		die("%v", err)
	}

	repo, err := git.RepoRoot(host, remotePath)
	if err != nil {
		die("not a git repo on remote: %s", remotePath)
	}

	cmdCreate(t, repo, root, cmd)
}

func cmdCreate(t transport.Transport, repo, root, cmd string) {
	name := worktree.GenerateName(filepath.Base(repo))
	if root == "" {
		root = worktree.DefaultRoot
	}
	wtDir := worktree.WorktreeDir(repo, root, name)

	if err := git.Pull(t.Host(), repo); err != nil {
		die("failed to pull: %v", err)
	}
	if err := git.WorktreeAdd(t.Host(), repo, name, wtDir); err != nil {
		die("failed to create worktree: %v", err)
	}

	sess := tmux.SessionName(name)
	if err := tmux.NewSession(t, sess, wtDir); err != nil {
		die("failed to create tmux session: %v", err)
	}

	if cmd != "" {
		tmux.SendKeys(t, sess, cmd)
	}

	if err := t.TmuxAttach(sess); err != nil {
		die("%v", err)
	}

	if err := git.Pull(t.Host(), repo); err != nil {
		fmt.Fprintf(os.Stderr, "warning: pull failed: %v\n", err)
	}
	printExitRow(t, worktree.Entry{
		Name:      name,
		Dir:       wtDir,
		Repo:      repo,
		Host:      t.Host(),
		CreatedAt: time.Now(),
	})
}

func cmdResume(entry worktree.Entry) {
	rt := transportFor(entry)

	host := entry.Host
	if err := git.Pull(host, entry.Repo); err != nil {
		fmt.Fprintf(os.Stderr, "warning: pull failed: %v\n", err)
	}

	sess := tmux.SessionName(entry.Name)
	if !tmux.HasSession(rt, sess) {
		if err := tmux.NewSession(rt, sess, entry.Dir); err != nil {
			die("failed to create tmux session: %v", err)
		}
	}

	if err := rt.TmuxAttach(sess); err != nil {
		die("%v", err)
	}

	if err := git.Pull(host, entry.Repo); err != nil {
		fmt.Fprintf(os.Stderr, "warning: pull failed: %v\n", err)
	}
	printExitRow(rt, entry)
}

func cmdLs() {
	all, pulled := discoverAll(true)

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
	display.SortRows(rows)
	display.PrintTable(rows)
}


func cmdDiff(args []string) {
	if len(args) == 0 {
		die("usage: wt diff <name>")
	}
	name := args[0]
	entry, ok := findWorktree(name)
	if !ok {
		die("worktree %q not found", name)
	}
	host := entry.Host

	if err := git.Pull(host, entry.Repo); err != nil {
		fmt.Fprintf(os.Stderr, "warning: pull failed: %v\n", err)
	}

	stat, err := git.DiffStat(host, entry.Dir, entry.Repo)
	if err != nil {
		die("diff: %v", err)
	}
	if stat == "" {
		fmt.Println("No changes on this branch.")
		return
	}
	fmt.Println(stat)

	isTTY := isTerminal()
	full, err := git.Diff(host, entry.Dir, entry.Repo, isTTY)
	if err != nil {
		die("diff: %v", err)
	}
	if isTTY {
		page(full)
	} else {
		fmt.Print(full)
	}
}

func isTerminal() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

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
  wt .                      Create a new worktree in cwd repo, open shell
  wt . <cmd>                Create a new worktree in cwd repo, run cmd
  wt <path> [cmd]           Create a new worktree in repo at path
  wt <host>:<path> [cmd]    Create a new worktree on remote host
  wt <name>                 Attach to an existing worktree
  wt ls                     List all worktrees (local and remote)
  wt diff <name>            Show changes on a worktree's branch
  wt rm                     Remove worktrees marked * in wt ls
  wt rm <name>              Remove a specific worktree

Status:
  attached    tmux client connected
  working     Process actively producing output
  dirty       Uncommitted changes in working tree
  merged *    Changes incorporated into upstream
  committed   Unique commits not yet in upstream
  idle        Session exists, no unique commits
  stale *     Session inactive >4 hours, no unique commits
  empty *     No tmux session for this worktree

Environment:
  WT_REMOTE_HOST            SSH hostname for wt ls remote discovery

Flags:
  --root <dir>              Directory for new worktrees, relative to repo root
                              (default: .. — sibling to the repo)
                              Example: --root .worktrees
  -h, --help                Show this help
`)
	fmt.Println(usage)
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "wt: "+format+"\n", args...)
	os.Exit(1)
}

func printExitRow(t transport.Transport, entry worktree.Entry) {
	entries := []worktree.Entry{entry}
	enrichEntries(entries, t)
	display.PrintTable([]display.Row{{
		Entry:  entries[0],
		Status: classifyStatus(entries[0]),
	}})
}

func transportFor(e worktree.Entry) transport.Transport {
	if e.Host == "" {
		return transport.NewLocal()
	}
	return transport.NewSSH(e.Host)
}
