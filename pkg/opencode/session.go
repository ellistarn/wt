package opencode

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"time"
)

// session is the minimal API response type from the OpenCode server.
type session struct {
	ID        string `json:"id"`
	Directory string `json:"directory"`
	Title     string `json:"title"`
	Time      struct {
		Created int64 `json:"created"`
		Updated int64 `json:"updated"`
	} `json:"time"`
}

// ServerURL returns the OpenCode server URL from environment or default.
func ServerURL() string {
	if u := os.Getenv("WT_SERVER"); u != "" {
		return u
	}
	port := os.Getenv("DEV_DESKTOP_TUNNEL_PORT")
	if port == "" {
		port = "9847"
	}
	return fmt.Sprintf("http://opencode.etarn:%s", port)
}

// FindLatestSession queries the OpenCode server for the most recent session
// in the given directory. Returns empty string if none found or server unreachable.
func FindLatestSession(serverURL, directory string) string {
	u := serverURL + "/session"
	if directory != "" {
		u += "?directory=" + url.QueryEscape(directory)
	}
	resp, err := httpGet(u)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	var sessions []session
	if err := json.NewDecoder(resp.Body).Decode(&sessions); err != nil {
		return ""
	}
	if len(sessions) == 0 {
		return ""
	}
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].Time.Updated > sessions[j].Time.Updated
	})
	return sessions[0].ID
}

func httpGet(u string) (*http.Response, error) {
	client := &http.Client{Timeout: 5 * time.Second}
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
