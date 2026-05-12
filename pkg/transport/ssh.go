package transport

import (
	"os/exec"
	"strings"

	"github.com/ellistarn/wt/pkg/ssh"
)

type SSH struct {
	host string
}

func NewSSH(host string) *SSH { return &SSH{host: host} }

func (s *SSH) Tmux(args ...string) (string, error) {
	// Non-interactive SSH doesn't source login profiles, so LANG is unset.
	// If this command starts the tmux server, the server permanently inherits
	// ASCII character-width tables, breaking all Unicode rendering in sessions.
	cmd := "LANG=C.UTF-8 tmux " + shellJoin(args)
	return ssh.Run(s.host, cmd)
}

func (s *SSH) TmuxAttach(session string) error {
	// LANG must be set for the client process too — tmux uses the attaching
	// client's locale to decide whether to output UTF-8 to the terminal.
	// Without it, the client inherits POSIX locale from sshd and tmux disables
	// UTF-8 output, garbling all multi-byte characters on screen.
	cmd := exec.Command("ssh", "-t",
		"-o", "ControlPath="+ssh.ControlPath,
		s.host, "LANG=C.UTF-8", "tmux", "attach-session", "-t", session)
	return runInteractive(cmd)
}

func (s *SSH) Exec(cmd string) (string, error) {
	return ssh.Run(s.host, cmd)
}

func (s *SSH) Host() string { return s.host }

func shellJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		if strings.ContainsAny(a, " \t\n'\"\\$`!#&|;(){}[]<>?*~") {
			quoted[i] = "'" + strings.ReplaceAll(a, "'", "'\\''") + "'"
		} else {
			quoted[i] = a
		}
	}
	return strings.Join(quoted, " ")
}
