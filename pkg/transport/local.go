package transport

import (
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
)

type Local struct{}

func NewLocal() *Local { return &Local{} }

func (l *Local) Tmux(args ...string) (string, error) {
	out, err := exec.Command("tmux", args...).Output()
	if err != nil {
		return strings.TrimSpace(string(out)), err
	}
	return strings.TrimSpace(string(out)), nil
}

func (l *Local) TmuxAttach(session string) error {
	cmd := exec.Command("tmux", "attach-session", "-t", session)
	return runInteractive(cmd)
}

func (l *Local) Exec(cmd string) (string, error) {
	out, err := exec.Command("bash", "-c", cmd).Output()
	if err != nil {
		return strings.TrimSpace(string(out)), err
	}
	return strings.TrimSpace(string(out)), nil
}

func (l *Local) Host() string { return "" }

// runInteractive runs a command with terminal ownership, forwarding signals.
func runInteractive(cmd *exec.Cmd) error {
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	signal.Ignore(syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTSTP)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGHUP, syscall.SIGTERM)

	if err := cmd.Start(); err != nil {
		return err
	}

	doneCh := make(chan error, 1)
	go func() { doneCh <- cmd.Wait() }()

	select {
	case err := <-doneCh:
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				os.Exit(exitErr.ExitCode())
			}
			return err
		}
		return nil
	case <-sigCh:
		cmd.Process.Signal(syscall.SIGTERM)
		<-doneCh
		os.Exit(1)
		return nil
	}
}
