package main

import (
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/ellistarn/wt/pkg/discover"
	"github.com/ellistarn/wt/pkg/git"
	"github.com/ellistarn/wt/pkg/opencode"
	"github.com/ellistarn/wt/pkg/ssh"
	"github.com/ellistarn/wt/pkg/worktree"
)

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

	// Enrich using sub-slices of all so in-place mutations are visible
	// in the returned slice.
	localEntries := all[:len(local)]
	remoteEntries := all[len(local):]

	// Run git fetch, local session enrichment, and remote session enrichment
	// concurrently. They touch different systems (git refs vs local HTTP vs
	// remote HTTP) and write disjoint fields/slices on the entries.
	var wg sync.WaitGroup
	var localErr, remoteErr error

	wg.Add(1)
	go func() {
		defer wg.Done()
		fetchRepos(all)
	}()

	if !remoteOnly {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := opencode.EnsureLocalServer(); err != nil {
				localErr = fmt.Errorf("local server: %w", err)
			} else if err := opencode.Enrich(opencode.LocalServerURL(), localEntries); err != nil {
				localErr = fmt.Errorf("local session query: %w", err)
			}
		}()
	}

	if host != "" && rr.err == nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := ssh.EnsureTunnel(host, opencode.TunnelPort(), opencode.ServerPort()); err != nil {
				remoteErr = fmt.Errorf("SSH tunnel: %w", err)
			} else if err := opencode.EnsureRemoteServer(host); err != nil {
				remoteErr = fmt.Errorf("remote server: %w", err)
			} else if err := opencode.Enrich(opencode.RemoteServerURL(), remoteEntries); err != nil {
				remoteErr = fmt.Errorf("remote session query: %w", err)
			}
		}()
	}

	wg.Wait()

	return all, errors.Join(localErr, remoteErr)
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
	var repos []key
	for _, e := range entries {
		k := key{hostFor(e), e.Repo}
		if !seen[k] {
			seen[k] = true
			repos = append(repos, k)
		}
	}
	var wg sync.WaitGroup
	for _, k := range repos {
		wg.Add(1)
		go func(k key) {
			defer wg.Done()
			git.Fetch(k.host, k.repo)
		}(k)
	}
	wg.Wait()
}

