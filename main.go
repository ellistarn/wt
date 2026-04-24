package main

import (
	"fmt"
	"os"
	"os/exec"
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
	repo, err := git.RepoRoot("")
	if err != nil {
		die("not in a git repo")
	}

	var name, wtDir string
	if len(args) == 0 {
		// Create new worktree
		name = worktree.GenerateName()
		wtDir = repo + "/.worktrees/" + name
		if err := git.WorktreeAdd("", repo, name); err != nil {
			die("failed to create worktree: %v", err)
		}
		fmt.Printf("Created worktree: %s\n", name)
		fmt.Printf("  resume:\n  wt %s\n", name)
	} else {
		name = args[0]
		wtDir = repo + "/.worktrees/" + name
		if !git.DirExists("", wtDir) {
			// Not found locally — try remote
			attachRemoteByName(name)
			return
		}
	}

	// Attach: exec opencode in the worktree
	if err := execOpencode(wtDir); err != nil {
		die("%v", err)
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
		localPath := args[0]
		fmt.Printf("Created worktree: %s\n", name)
		fmt.Printf("  resume:\n  wt -r %s %s\n", localPath, name)
	} else {
		name = args[1]
		wtDir = repo + "/.worktrees/" + name
		if !git.DirExists(host, wtDir) {
			die("worktree not found on remote: %s", wtDir)
		}
	}

	// Attach: find most recent session, then opencode attach
	serverURL := opencode.ServerURL()
	sessionID := opencode.FindLatestSession(serverURL, wtDir)
	if err := execOpencodeAttach(serverURL, wtDir, sessionID); err != nil {
		die("%v", err)
	}
}

// attachRemoteByName searches remote worktrees for one matching the given name
// and attaches to it. Dies if DEV_DESKTOP_HOST is unset or the name isn't found.
func attachRemoteByName(name string) {
	host := os.Getenv("DEV_DESKTOP_HOST")
	if host == "" {
		die("worktree %q not found locally and DEV_DESKTOP_HOST is not set", name)
	}

	entries := discover.ListRemote(host)
	for _, e := range entries {
		if e.Name == name {
			serverURL := opencode.ServerURL()
			sessionID := opencode.FindLatestSession(serverURL, e.Dir)
			if err := execOpencodeAttach(serverURL, e.Dir, sessionID); err != nil {
				die("%v", err)
			}
			return // unreachable — ExecAttach calls syscall.Exec
		}
	}
	die("worktree %q not found locally or on remote", name)
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
  wt <name>                 Attach to an existing local worktree
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

// execOpencode replaces the current process with opencode in the given directory.
func execOpencode(dir string) error {
	binary, err := exec.LookPath("opencode")
	if err != nil {
		return fmt.Errorf("opencode not found in PATH")
	}

	if err := os.Chdir(dir); err != nil {
		return fmt.Errorf("cannot cd to %s: %w", dir, err)
	}

	return syscall.Exec(binary, []string{"opencode"}, os.Environ())
}

// execOpencodeAttach replaces the current process with opencode attach.
func execOpencodeAttach(serverURL, dir, sessionID string) error {
	binary, err := exec.LookPath("opencode")
	if err != nil {
		return fmt.Errorf("opencode not found in PATH")
	}

	args := []string{"opencode", "attach", serverURL, "--dir", dir}
	if sessionID != "" {
		args = append(args, "--session", sessionID)
	}

	return syscall.Exec(binary, args, os.Environ())
}
