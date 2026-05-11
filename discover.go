package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ellistarn/wt/pkg/discover"
	"github.com/ellistarn/wt/pkg/git"
	"github.com/ellistarn/wt/pkg/tmux"
	"github.com/ellistarn/wt/pkg/transport"
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
	suffix := "-" + name
	for _, e := range all {
		if strings.HasSuffix(e.Name, suffix) {
			return e, true
		}
	}
	return worktree.Entry{}, false
}

// discoverAll discovers worktrees, enriches them with tmux session data,
// and starts pulls for each unique repo. Returns entries and pull handles.
func discoverAll(pull bool) ([]worktree.Entry, pullResult) {
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

	// Enrich with tmux session data
	localEntries := all[:len(local)]
	remoteEntries := all[len(local):]

	localT := transport.NewLocal()
	enrichEntries(localEntries, localT)

	if host != "" && rr.err == nil {
		remoteT := transport.NewSSH(host)
		enrichEntries(remoteEntries, remoteT)
	}

	// Start pulls
	var pulled pullResult
	if pull {
		pulled = startPullRepos(all)
	} else {
		pulled = make(pullResult)
	}

	return all, pulled
}

// enrichEntries enriches worktree entries with tmux session data (attached, working, title, activity).
func enrichEntries(entries []worktree.Entry, t transport.Transport) {
	sessions := tmux.ListSessions(t)
	sessionSet := make(map[string]bool, len(sessions))
	for _, s := range sessions {
		sessionSet[s] = true
	}

	attached := tmux.AttachedSessions(t)

	for i := range entries {
		sess := tmux.SessionName(entries[i].Name)
		if !sessionSet[sess] {
			continue
		}

		entries[i].Attached = attached[sess]
		entries[i].Title = tmux.PaneTitle(t, sess)

		activity := tmux.WindowActivity(t, sess)
		if !activity.IsZero() {
			entries[i].UpdatedAt = activity
		}

		if !activity.IsZero() && time.Since(activity) < tmux.ActivityThreshold {
			entries[i].Status = "working"
		} else {
			entries[i].Status = "idle"
		}

		if entries[i].Status == "idle" && !entries[i].UpdatedAt.IsZero() &&
			time.Since(entries[i].UpdatedAt) > worktree.StaleThreshold {
			entries[i].Status = "stale"
		}
	}
}

// pullResult holds per-repo done channels from a non-blocking pull.
type pullResult map[repoKey]<-chan struct{}

func (f pullResult) Wait(e worktree.Entry) {
	if ch, ok := f[repoKey{e.Host, e.Repo}]; ok {
		<-ch
	}
}

type repoKey struct{ host, repo string }

func startPullRepos(entries []worktree.Entry) pullResult {
	seen := make(map[repoKey]bool)
	result := make(pullResult)
	for _, e := range entries {
		k := repoKey{e.Host, e.Repo}
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
