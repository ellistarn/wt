package opencode

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"time"

	"github.com/ellistarn/wt/pkg/worktree"
)

// StaleThreshold is the duration after which a session with no recent activity
// is considered stale.
const StaleThreshold = 12 * time.Hour

// Session is the API response type from the OpenCode server.
type Session struct {
	ID        string `json:"id"`
	Directory string `json:"directory"`
	Title     string `json:"title"`
	Time      struct {
		Created int64 `json:"created"`
		Updated int64 `json:"updated"`
	} `json:"time"`
}

// message is the minimal structure for a session message from the server API.
type message struct {
	Info struct {
		Role   string `json:"role"`
		Tokens struct {
			Total int `json:"total"`
		} `json:"tokens"`
		Time struct {
			Completed int64 `json:"completed"`
		} `json:"time"`
	} `json:"info"`
}

// sessionStatus captures the working/idle signal from a session's messages.
type sessionStatus struct {
	tokens    int  // context window size (last assistant message with non-zero total)
	streaming bool // true if the most recent assistant message has not completed
}

// LocalServerURL returns the local OpenCode server URL.
func LocalServerURL() string {
	return fmt.Sprintf("http://localhost:%d", ServerPort())
}

// RemoteServerURL returns the remote OpenCode server URL (through the SSH tunnel).
func RemoteServerURL() string {
	return fmt.Sprintf("http://localhost:%d", TunnelPort())
}

// CheckHealth verifies that the OpenCode server is reachable.
func CheckHealth(serverURL string) error {
	resp, err := httpGet(serverURL + "/global/health")
	if err != nil {
		return fmt.Errorf("opencode server not reachable at %s", serverURL)
	}
	resp.Body.Close()
	return nil
}

// checkHealthFast is a quick probe for use before batch operations.
func checkHealthFast(serverURL string) error {
	resp, err := httpGetTimeout(serverURL+"/global/health", 1*time.Second)
	if err != nil {
		return fmt.Errorf("opencode server not reachable at %s", serverURL)
	}
	resp.Body.Close()
	return nil
}

// FindLatestSession queries the OpenCode server for the most recent session
// in the given directory. Returns empty string if none found or on error.
// Best-effort — used for session resumption where failure means a new session.
func FindLatestSession(serverURL, directory string) string {
	s, _ := QuerySession(serverURL, directory)
	if s == nil {
		return ""
	}
	return s.ID
}

// QuerySession queries the OpenCode server for the most recent session
// in the given directory. Returns nil if none found.
func QuerySession(serverURL, directory string) (*Session, error) {
	sessions, err := listSessions(serverURL, directory)
	if err != nil {
		return nil, err
	}
	if len(sessions) == 0 {
		return nil, nil
	}
	return &sessions[0], nil
}

// Enrich enriches worktree entries with session data from the OpenCode server.
// Queries each entry's session by directory (crossing project boundaries) and
// fetches message status for sessions that may be actively streaming.
func Enrich(serverURL string, entries []worktree.Entry) error {
	if err := checkHealthFast(serverURL); err != nil {
		return err
	}

	// Query session + message status per entry in parallel (bounded to 8).
	// Per-directory queries cross OpenCode project boundaries, unlike the
	// bulk /session endpoint which is scoped to the active project.
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i := range entries {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			s, _ := QuerySession(serverURL, entries[i].Dir)
			if s == nil {
				return
			}
			entries[i].SessionID = s.ID
			entries[i].Title = s.Title
			entries[i].UpdatedAt = time.UnixMilli(s.Time.Updated)

			if time.Since(entries[i].UpdatedAt) > StaleThreshold {
				entries[i].Status = "stale"
				return
			}

			status := fetchSessionStatus(serverURL, entries[i].SessionID)
			entries[i].Tokens = status.tokens
			if status.streaming {
				entries[i].Status = "working"
			} else {
				entries[i].Status = "idle"
			}
		}(i)
	}
	wg.Wait()

	// Detect locally attached TUI clients.
	attached := AttachedDirs()
	for i := range entries {
		if attached[entries[i].Dir] {
			entries[i].Attached = true
		}
	}

	return nil
}

// listSessions fetches sessions from the server, optionally filtered by directory.
// Returns sessions sorted by most recently updated first.
func listSessions(serverURL, directory string) ([]Session, error) {
	u := serverURL + "/session?limit=1000"
	if directory != "" {
		u += "&directory=" + url.QueryEscape(directory)
	}
	resp, err := httpGet(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var sessions []Session
	if err := json.NewDecoder(resp.Body).Decode(&sessions); err != nil {
		return nil, fmt.Errorf("decode session response: %w", err)
	}
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].Time.Updated > sessions[j].Time.Updated
	})
	return sessions, nil
}

// fetchSessionStatus returns the context window size and streaming state for
// the session. It walks backwards through the message list: the first (most
// recent) assistant message determines streaming (completed == 0), and the
// first assistant message with non-zero tokens.Total gives the token count.
func fetchSessionStatus(serverURL, sessionID string) sessionStatus {
	resp, err := httpGet(serverURL + "/session/" + sessionID + "/message")
	if err != nil {
		return sessionStatus{}
	}
	defer resp.Body.Close()

	var messages []message
	if err := json.NewDecoder(resp.Body).Decode(&messages); err != nil {
		return sessionStatus{}
	}

	var result sessionStatus
	foundStreaming := false
	foundTokens := false
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Info.Role != "assistant" {
			continue
		}
		if !foundStreaming {
			result.streaming = messages[i].Info.Time.Completed == 0
			foundStreaming = true
		}
		if !foundTokens && messages[i].Info.Tokens.Total > 0 {
			result.tokens = messages[i].Info.Tokens.Total
			foundTokens = true
		}
		if foundStreaming && foundTokens {
			break
		}
	}
	return result
}

func httpGet(u string) (*http.Response, error) {
	return httpGetTimeout(u, 5*time.Second)
}

func httpGetTimeout(u string, timeout time.Duration) (*http.Response, error) {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(u)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, u)
	}
	return resp, nil
}
