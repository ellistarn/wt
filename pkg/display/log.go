package display

import (
	"fmt"
	"os"
)

// LogCmd prints a command invocation to stderr with a prompt prefix.
func LogCmd(line string) {
	fmt.Fprintf(os.Stderr, "> %s\n", line)
}

// LogOutput prints command output to stderr.
func LogOutput(output string) {
	if output != "" {
		fmt.Fprintln(os.Stderr, output)
	}
}
