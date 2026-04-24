package opencode

import (
	"encoding/json"
	"net/url"
	"sync"
	"time"

	"github.com/ellistarn/wt/pkg/worktree"
)

// sessionMetrics holds token count and derived status from message data.
type sessionMetrics struct {
	tokens       int
	status       string    // "working" or "idle"
	lastActivity time.Time // when the session was last active
}

// messageResponse is the API response for GET /session/:id/message.
type messageResponse struct {
	Info struct {
		Role   string `json:"role"`
		Time   *struct {
			Created   int64  `json:"created"`
			Completed *int64 `json:"completed,omitempty"`
		} `json:"time,omitempty"`
		Tokens *struct {
			Total int `json:"total"`
		} `json:"tokens,omitempty"`
	} `json:"info"`
}

// Enrich populates worktree entries with session data from an OpenCode server.
func Enrich(serverURL string, entries []worktree.Entry) {
	if len(entries) == 0 {
		return
	}

	// Quick health check — if the server is down, skip enrichment entirely
	// rather than waiting for N individual HTTP timeouts.
	if err := checkHealthFast(serverURL); err != nil {
		return
	}

	// Fetch sessions per directory concurrently. The session API is
	// project-scoped, so an unfiltered GET /session only returns sessions
	// for the server's own project. The directory parameter crosses
	// project boundaries.
	byDir := fetchSessionsByDir(serverURL, entries)
	if len(byDir) == 0 {
		return
	}

	// Match sessions to entries and collect IDs for metric fetching.
	var sessionIDs []string
	entrySessionMap := make(map[int]string) // entry index -> session ID
	for i := range entries {
		s, ok := byDir[entries[i].Dir]
		if !ok {
			continue
		}
		entries[i].SessionID = s.ID
		entries[i].Title = s.Title
		entries[i].UpdatedAt = time.UnixMilli(s.Time.Updated)
		sessionIDs = append(sessionIDs, s.ID)
		entrySessionMap[i] = s.ID
	}

	// Fetch tokens and derive status from message data concurrently.
	// Status is derived from whether the last assistant message is still
	// streaming (no completion time), avoiding the project-scoped
	// /session/status endpoint.
	metricsBySession := fetchMetricsConcurrently(serverURL, sessionIDs)
	for i, sid := range entrySessionMap {
		if m, ok := metricsBySession[sid]; ok {
			entries[i].Tokens = m.tokens
			entries[i].Status = m.status
			if !m.lastActivity.IsZero() {
				entries[i].UpdatedAt = m.lastActivity
			}
		} else {
			entries[i].Status = "idle"
		}
	}

	// Detect locally attached TUI clients.
	attached := AttachedDirs()
	for i := range entries {
		if count, ok := attached[entries[i].Dir]; ok && count > 0 {
			entries[i].Attached = true
		}
	}
}

// fetchSessionsByDir queries GET /session?directory=<dir> for each entry
// concurrently and returns the most recent session per directory.
func fetchSessionsByDir(serverURL string, entries []worktree.Entry) map[string]session {
	result := make(map[string]session)
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, e := range entries {
		wg.Add(1)
		go func(dir string) {
			defer wg.Done()
			sessions := fetchSessions(serverURL, dir)
			if len(sessions) == 0 {
				return
			}
			// Pick the most recently updated session for this directory.
			best := sessions[0]
			for _, s := range sessions[1:] {
				if s.Time.Updated > best.Time.Updated {
					best = s
				}
			}
			mu.Lock()
			result[dir] = best
			mu.Unlock()
		}(e.Dir)
	}

	wg.Wait()
	return result
}

// fetchSessions calls GET /session?directory=<dir> and returns matching sessions.
func fetchSessions(serverURL, directory string) []session {
	u := serverURL + "/session"
	if directory != "" {
		u += "?directory=" + url.QueryEscape(directory)
	}
	resp, err := httpGet(u)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	var sessions []session
	if err := json.NewDecoder(resp.Body).Decode(&sessions); err != nil {
		return nil
	}
	return sessions
}

// fetchMetricsConcurrently fetches message data for each session concurrently,
// deriving token counts and working/idle status.
func fetchMetricsConcurrently(serverURL string, sessionIDs []string) map[string]sessionMetrics {
	result := make(map[string]sessionMetrics)
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, sid := range sessionIDs {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			m := fetchSessionMetrics(serverURL, id)
			mu.Lock()
			result[id] = m
			mu.Unlock()
		}(sid)
	}

	wg.Wait()
	return result
}

// fetchSessionMetrics calls GET /session/:id/message to get the context
// window size (total tokens from the last assistant message) and status.
// A session is "working" if the last assistant message has no completion
// time (the agent is still streaming).
func fetchSessionMetrics(serverURL, sessionID string) sessionMetrics {
	resp, err := httpGet(serverURL + "/session/" + sessionID + "/message")
	if err != nil {
		return sessionMetrics{status: "idle"}
	}
	defer resp.Body.Close()

	var messages []messageResponse
	if err := json.NewDecoder(resp.Body).Decode(&messages); err != nil {
		return sessionMetrics{status: "idle"}
	}

	return computeMetrics(messages)
}

// computeMetrics derives token count, status, and last activity from a list
// of messages. Walks backwards through assistant messages:
//   - Status comes from the last assistant message (streaming = working).
//   - Tokens come from the last message with a non-zero total. A streaming
//     message reports total=0 until completion, so we fall back to the
//     previous completed message.
func computeMetrics(messages []messageResponse) sessionMetrics {
	tokens := 0
	status := "idle"
	statusSet := false
	var lastActivity time.Time
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Info.Role != "assistant" {
			continue
		}
		// Status: only set once (from the very last assistant message).
		if !statusSet {
			statusSet = true
			if messages[i].Info.Time != nil {
				if messages[i].Info.Time.Completed == nil {
					status = "working"
					lastActivity = time.Now()
				} else {
					lastActivity = time.UnixMilli(*messages[i].Info.Time.Completed)
				}
			}
		}
		// Tokens: take the first non-zero total we find.
		if tokens == 0 && messages[i].Info.Tokens != nil && messages[i].Info.Tokens.Total > 0 {
			tokens = messages[i].Info.Tokens.Total
		}
		// Stop once we have both.
		if tokens > 0 && statusSet {
			break
		}
	}

	return sessionMetrics{tokens: tokens, status: status, lastActivity: lastActivity}
}
