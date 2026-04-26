package cmdlog

import (
	"fmt"
	"os"
	"sync/atomic"
)

// logged tracks whether any command output has been written to stderr.
var logged atomic.Bool

// HasLogged reports whether any command output was emitted.
func HasLogged() bool { return logged.Load() }

// LogCmd prints a command invocation to stderr with a prompt prefix.
func LogCmd(line string) {
	logged.Store(true)
	fmt.Fprintf(os.Stderr, "> %s\n", line)
}

// LogOutput prints command output to stderr.
func LogOutput(output string) {
	if output != "" {
		logged.Store(true)
		fmt.Fprintln(os.Stderr, output)
	}
}
