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
// Git pull runs concurrently with enrichment; per-repo done channels are
// returned so callers can gate classification on each repo's pull completing.
// Returns any enrichment error — callers that make safety decisions must
// check this; callers that only display can ignore it.
func discoverAll(pull bool) ([]worktree.Entry, pullResult, error) {
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

	// Discover in parallel, then pull and enrich.
	local := <-localCh
	rr := <-remoteCh
	if rr.err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", rr.err)
	}

	all := append(local, rr.entries...)

	// Enrich using sub-slices of all so in-place mutations are visible
	// in the returned slice.
	localEntries := all[:len(local)]
	remoteEntries := all[len(local):]

	// Run git pull and session enrichment concurrently. Pull is non-blocking —
	// per-repo done channels are returned for callers to wait on per-entry,
	// overlapping pull with classification instead of serializing them.
	var wg sync.WaitGroup
	var localErr, remoteErr error

	var pulled pullResult
	if pull {
		pulled = startPullRepos(all)
	} else {
		pulled = make(pullResult)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := opencode.EnsureLocalServer(); err != nil {
			localErr = fmt.Errorf("local server: %w", err)
		} else if err := opencode.Enrich(opencode.LocalServerURL(), localEntries); err != nil {
			localErr = fmt.Errorf("local session query: %w", err)
		}
	}()

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

	return all, pulled, errors.Join(localErr, remoteErr)
}

// hostFor returns the SSH host for an entry, or "" for local entries.
func hostFor(e worktree.Entry) string {
	return e.Host
}

// pullResult holds per-repo done channels from a non-blocking pull.
// Wait blocks until the given entry's repo has been pulled.
type pullResult map[repoKey]<-chan struct{}

func (f pullResult) Wait(e worktree.Entry) {
	if ch, ok := f[repoKey{hostFor(e), e.Repo}]; ok {
		<-ch
	}
}

type repoKey struct{ host, repo string }

// startPullRepos kicks off git pull for each unique repo and returns
// immediately. Each repo gets its own goroutine; the returned channels
// close when each repo's pull completes. Warnings are printed to stderr
// for any repos that fail to pull.
func startPullRepos(entries []worktree.Entry) pullResult {
	seen := make(map[repoKey]bool)
	result := make(pullResult)
	for _, e := range entries {
		k := repoKey{hostFor(e), e.Repo}
		if seen[k] {
			continue
		}
		seen[k] = true
		ch := make(chan struct{})
		result[k] = ch
		go func(k repoKey, ch chan struct{}) {
			if err := git.Pull(k.host, k.repo); err != nil {
				fmt.Fprintf(os.Stderr, "warning: pull failed for %s: %v\n", k.repo, err)
			}
			close(ch)
		}(k, ch)
	}
	return result
}

