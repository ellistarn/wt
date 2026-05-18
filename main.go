package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ellistarn/wt/pkg/display"
	"github.com/ellistarn/wt/pkg/git"
	"github.com/ellistarn/wt/pkg/provider"
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

// isPath returns true if the argument looks like a path.
func isPath(arg string) bool {
	return arg == "." || strings.HasPrefix(arg, "./") ||
		strings.HasPrefix(arg, "/") || strings.HasPrefix(arg, "~/") ||
		strings.Contains(arg, "/")
}

func cmdDispatch(args []string, root string) {
	first := args[0]

	if isPath(first) {
		cmd := ""
		if len(args) > 1 {
			cmd = strings.Join(args[1:], " ")
		}
		cmdCreate(first, root, cmd)
		return
	}

	// Try to resume an existing worktree
	if entry, ok := findWorktree(first); ok {
		cmdResume(entry)
		return
	}

	die("worktree %q not found", first)
}

func cmdCreate(path, root, cmd string) {
	repo, err := git.RepoRoot(path)
	if err != nil {
		die("not in a git repo: %s", path)
	}

	name := worktree.GenerateName(filepath.Base(repo))
	if root == "" {
		root = worktree.DefaultRoot
	}
	wtDir := worktree.WorktreeDir(repo, root, name)

	if err := git.Pull(repo); err != nil {
		die("failed to pull: %v", err)
	}
	if err := git.WorktreeAdd(repo, name, wtDir); err != nil {
		die("failed to create worktree: %v", err)
	}

	// Resolve command: explicit arg > $WT_CMD > "opencode"
	if cmd == "" {
		cmd = os.Getenv("WT_CMD")
	}
	if cmd == "" {
		cmd = "opencode"
	}

	// Save metadata
	worktree.WriteMetadata(repo, name, worktree.Metadata{Cmd: cmd})

	runAgent(wtDir, cmd, repo, name)

	if err := git.Pull(repo); err != nil {
		fmt.Fprintf(os.Stderr, "warning: pull failed: %v\n", err)
	}
	printExitRow(worktree.Entry{
		Name:      name,
		Branch:    name,
		Dir:       wtDir,
		Repo:      repo,
		CreatedAt: time.Now(),
	})
}

func cmdResume(entry worktree.Entry) {
	if err := git.Pull(entry.Repo); err != nil {
		fmt.Fprintf(os.Stderr, "warning: pull failed: %v\n", err)
	}

	// Read saved command, fall back to $WT_CMD, then "opencode"
	meta := worktree.ReadMetadata(entry.Repo, entry.Name)
	cmd := meta.Cmd
	if cmd == "" {
		cmd = os.Getenv("WT_CMD")
	}
	if cmd == "" {
		cmd = "opencode"
	}

	// For opencode, find the existing session and resume it.
	if isOpenCodeCmd(cmd) {
		if id := findOpenCodeSessionID(entry.Dir); id != "" {
			cmd += " --session " + id
		}
	}

	runAgent(entry.Dir, cmd, entry.Repo, entry.Name)

	if err := git.Pull(entry.Repo); err != nil {
		fmt.Fprintf(os.Stderr, "warning: pull failed: %v\n", err)
	}
	printExitRow(entry)
}

func runAgent(dir, cmd string, repo, name string) {
	parts := strings.Fields(cmd)
	c := exec.Command(parts[0], parts[1:]...)
	c.Dir = dir
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	if err := c.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to start %s: %v\n", parts[0], err)
		return
	}

	go func() {
		for sig := range sigCh {
			if c.Process != nil {
				c.Process.Signal(sig)
			}
		}
	}()

	c.Wait()
	signal.Stop(sigCh)
	close(sigCh)
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

	if err := git.Pull(entry.Repo); err != nil {
		fmt.Fprintf(os.Stderr, "warning: pull failed: %v\n", err)
	}

	stat, err := git.DiffStat(entry.Dir, entry.Repo)
	if err != nil {
		die("diff: %v", err)
	}
	if stat == "" {
		fmt.Println("No changes on this branch.")
		return
	}
	fmt.Println(stat)

	isTTY := isTerminal()
	full, err := git.Diff(entry.Dir, entry.Repo, isTTY)
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
wt — worktree manager

Usage:
  wt .                      Create a new worktree in cwd repo
  wt <path>                 Create a new worktree in repo at path
  wt <path> <cmd>           Create worktree, run cmd (default: $WT_CMD or opencode)
  wt <name>                 Resume an existing worktree
  wt ls                     List all worktrees
  wt diff <name>            Show changes on a worktree's branch
  wt rm                     Remove worktrees marked * in wt ls
  wt rm <name>              Remove a specific worktree

Environment:
  WT_CMD                    Default agent command (default: opencode)

Status:
  active      Agent session running
  dirty       Uncommitted changes in working tree
  merged *    Changes incorporated into upstream
  committed   Unique commits not yet in upstream
  idle        Has session history, no unique commits
  stale *     Inactive >4 hours, no unique commits
  empty *     No session for this worktree

Flags:
  --root <dir>              Directory for new worktrees, relative to repo root
                              (default: .. — sibling to the repo)
  -h, --help                Show this help
`)
	fmt.Println(usage)
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "wt: "+format+"\n", args...)
	os.Exit(1)
}

func printExitRow(entry worktree.Entry) {
	enrichAll([]worktree.Entry{entry})
	display.PrintTable([]display.Row{{
		Entry:  entry,
		Status: classifyStatus(entry),
	}})
}

// isOpenCodeCmd reports whether cmd invokes the opencode agent.
func isOpenCodeCmd(cmd string) bool {
	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return false
	}
	return filepath.Base(parts[0]) == "opencode"
}

// findOpenCodeSessionID returns the most recent OpenCode session ID for the
// given directory, or "" if no session is found.
func findOpenCodeSessionID(dir string) string {
	return provider.FetchOpenCodeSessions().MatchID(dir)
}
