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
	cmd := "tmux " + shellJoin(args)
	return ssh.Run(s.host, cmd)
}

func (s *SSH) TmuxAttach(session string) error {
	cmd := exec.Command("ssh", "-t",
		"-o", "ControlPath="+ssh.ControlPath,
		s.host, "tmux", "attach-session", "-t", session)
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
