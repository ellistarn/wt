package transport

// Transport abstracts how wt reaches a target machine to run commands.
type Transport interface {
	// Tmux executes a tmux command and returns its stdout.
	Tmux(args ...string) (string, error)

	// TmuxAttach attaches the local terminal to a tmux session interactively.
	TmuxAttach(session string) error

	// Exec runs a shell command string and returns stdout.
	Exec(cmd string) (string, error)

	// Host returns the hostname ("" for local).
	Host() string
}
