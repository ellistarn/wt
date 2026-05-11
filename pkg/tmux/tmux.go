package tmux

import (
	"strconv"
	"strings"
	"time"

	"github.com/ellistarn/wt/pkg/transport"
)

const Prefix = "wt/"

// SessionName returns the tmux session name for a worktree.
func SessionName(wtName string) string {
	return Prefix + wtName
}

// HasSession checks if a tmux session exists.
func HasSession(t transport.Transport, name string) bool {
	_, err := t.Tmux("has-session", "-t", name)
	return err == nil
}

// NewSession creates a detached tmux session in the given directory.
func NewSession(t transport.Transport, name, dir string) error {
	_, err := t.Tmux("new-session", "-d", "-s", name, "cd '"+dir+"' && exec $SHELL")
	return err
}

// SendKeys sends a command string to a tmux session pane.
func SendKeys(t transport.Transport, session, cmd string) error {
	_, err := t.Tmux("send-keys", "-t", session, cmd, "Enter")
	return err
}

// KillSession kills a tmux session. Returns nil if the session doesn't exist.
func KillSession(t transport.Transport, session string) error {
	_, err := t.Tmux("kill-session", "-t", session)
	return err
}

// ListSessions returns all tmux session names matching the wt/ prefix.
func ListSessions(t transport.Transport) []string {
	out, err := t.Tmux("list-sessions", "-F", "#{session_name}")
	if err != nil {
		return nil
	}
	var sessions []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, Prefix) {
			sessions = append(sessions, line)
		}
	}
	return sessions
}

// AttachedSessions returns the set of session names that have clients attached.
func AttachedSessions(t transport.Transport) map[string]bool {
	out, err := t.Tmux("list-clients", "-F", "#{client_session}")
	if err != nil {
		return nil
	}
	result := make(map[string]bool)
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			result[line] = true
		}
	}
	return result
}

// ActivityThreshold is how recently a window must have received output to be considered active.
const ActivityThreshold = 5 * time.Second

// WindowActivity returns the last time the session's window received output.
func WindowActivity(t transport.Transport, session string) time.Time {
	out, err := t.Tmux("display-message", "-t", session, "-p", "#{window_activity}")
	if err != nil {
		return time.Time{}
	}
	ts, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(ts, 0)
}

// PaneTitle returns the terminal title set by the process in the session's pane.
func PaneTitle(t transport.Transport, session string) string {
	out, err := t.Tmux("display-message", "-t", session, "-p", "#{pane_title}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}
