package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

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

	if remote {
		cmdRemote(remaining)
	} else {
		cmdLocal(remaining)
	}
}

// cmdLocal handles: wt [name]
func cmdLocal(args []string) {
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
		fmt.Printf("Created worktree: %s\n", name)
		fmt.Printf("wt %s\n", name)
		if err := runOpencode(wtDir); err != nil {
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
		if err := runOpencode(entry.Dir); err != nil {
			die("%v", err)
		}
	} else {
		serverURL := opencode.ServerURL()
		sessionID := opencode.FindLatestSession(serverURL, entry.Dir)
		if err := runOpencodeAttach(serverURL, entry.Dir, sessionID); err != nil {
			die("%v", err)
		}
	}
}

// cmdRemote handles: wt -r <path> [name]
func cmdRemote(args []string) {
	if len(args) == 0 {
		die("remote mode requires a repo path: wt -r <path> [name]")
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

	var name, wtDir string
	if len(args) < 2 {
		// Create new worktree
		name = worktree.GenerateName()
		wtDir = repo + "/.worktrees/" + name
		if err := git.WorktreeAdd(host, repo, name); err != nil {
			die("failed to create remote worktree: %v", err)
		}
		fmt.Printf("Created worktree: %s\n", name)
		fmt.Printf("wt %s\n", name)
	} else {
		name = args[1]
		wtDir = repo + "/.worktrees/" + name
		if !git.DirExists(host, wtDir) {
			die("worktree not found on remote: %s", wtDir)
		}
	}

	serverURL := opencode.ServerURL()
	sessionID := opencode.FindLatestSession(serverURL, wtDir)
	if err := runOpencodeAttach(serverURL, wtDir, sessionID); err != nil {
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
	host := os.Getenv("DEV_DESKTOP_HOST")

	// Run local and remote discovery concurrently
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

	local := <-localCh
	remote := <-remoteCh

	opencode.EnrichLocal(local)
	if host != "" {
		opencode.EnrichRemote(host, remote)
	}

	all := append(local, remote...)
	worktree.Sort(all)

	if len(all) == 0 {
		fmt.Println("No worktrees found.")
		return
	}
	display.PrintTable(all)
}

func printUsage() {
	usage := strings.TrimSpace(`
wt — worktree session manager

Usage:
  wt                        Create a new local worktree and attach
  wt <name>                 Attach to an existing worktree (local or remote)
  wt -r <path>              Create a new remote worktree and attach
  wt -r <path> <name>       Attach to an existing remote worktree
  wt ls                     List all worktrees (local and remote)
  wt -r ls                  List remote worktrees only

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

// runOpencode runs opencode as a subprocess in the given directory, clearing the
// terminal on exit to remove opencode's startup banner from scrollback.
func runOpencode(dir string) error {
	binary, err := exec.LookPath("opencode")
	if err != nil {
		return fmt.Errorf("opencode not found in PATH")
	}
	if err := os.Chdir(dir); err != nil {
		return fmt.Errorf("cannot cd to %s: %w", dir, err)
	}
	return runAndClear(exec.Command(binary))
}

// runOpencodeAttach runs opencode attach as a subprocess, clearing the terminal
// on exit to remove opencode's startup banner from scrollback.
func runOpencodeAttach(serverURL, dir, sessionID string) error {
	binary, err := exec.LookPath("opencode")
	if err != nil {
		return fmt.Errorf("opencode not found in PATH")
	}
	args := []string{"attach", serverURL, "--dir", dir}
	if sessionID != "" {
		args = append(args, "--session", sessionID)
	}
	return runAndClear(exec.Command(binary, args...))
}

// runAndClear runs a TUI command as a subprocess, letting it own the terminal.
// Terminal signals are ignored in the parent so the child handles them.
// After the child exits, the terminal is cleared to remove pre-TUI output.
func runAndClear(cmd *exec.Cmd) error {
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Let the child handle all terminal signals; parent just waits.
	signal.Ignore(syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTSTP)

	err := cmd.Run()

	// Clear screen to wipe opencode's startup banner from scrollback.
	fmt.Print("\033[2J\033[H")

	if err != nil {
		// Forward the child's exit code.
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		return err
	}
	return nil
}
