package opencode

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"time"

	"github.com/ellistarn/wt/pkg/worktree"
)

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
// in the given directory. Returns empty string if none found or server unreachable.
func FindLatestSession(serverURL, directory string) string {
	s := QuerySession(serverURL, directory)
	if s == nil {
		return ""
	}
	return s.ID
}

// QuerySession queries the OpenCode server for the most recent session
// in the given directory. Returns nil if none found or server unreachable.
func QuerySession(serverURL, directory string) *Session {
	sessions := listSessions(serverURL, directory)
	if len(sessions) == 0 {
		return nil
	}
	return &sessions[0]
}

// Enrich enriches worktree entries with session data from the OpenCode server.
// Fetches tokens for sessions that are actively streaming.
func Enrich(serverURL string, entries []worktree.Entry) error {
	if err := checkHealthFast(serverURL); err != nil {
		return err
	}

	for i := range entries {
		s := QuerySession(serverURL, entries[i].Dir)
		if s == nil {
			continue
		}
		entries[i].SessionID = s.ID
		entries[i].Title = s.Title
		entries[i].UpdatedAt = time.UnixMilli(s.Time.Updated)

		if time.Since(entries[i].UpdatedAt) < 30*time.Second {
			entries[i].Status = "working"
			entries[i].Tokens = fetchSessionTokens(serverURL, s.ID)
		} else {
			entries[i].Status = "idle"
		}
	}

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
func listSessions(serverURL, directory string) []Session {
	u := serverURL + "/session?limit=1000"
	if directory != "" {
		u += "&directory=" + url.QueryEscape(directory)
	}
	resp, err := httpGet(u)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	var sessions []Session
	if err := json.NewDecoder(resp.Body).Decode(&sessions); err != nil {
		return nil
	}
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].Time.Updated > sessions[j].Time.Updated
	})
	return sessions
}

// fetchSessionTokens returns the total tokens used across all assistant messages
// in a session. Also returns whether the session is actively streaming (last
// assistant message has no completion time).
func fetchSessionTokens(serverURL, sessionID string) int {
	resp, err := httpGet(serverURL + "/session/" + sessionID + "/message")
	if err != nil {
		return 0
	}
	defer resp.Body.Close()

	var messages []message
	if err := json.NewDecoder(resp.Body).Decode(&messages); err != nil {
		return 0
	}

	var total int
	for _, m := range messages {
		if m.Info.Role == "assistant" {
			total += m.Info.Tokens.Total
		}
	}
	return total
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
