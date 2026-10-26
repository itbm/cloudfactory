// Command cf controls a Cloudfactory panel from the command line (FR-1402).
// It uses only the public API, the same as the web panel.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
)

// version is set at build time with -ldflags "-X main.version=0.1.0".
var version = "0.0.0-dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "cf:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	a := &app{out: stdout, err: stderr}
	return a.dispatch(ctx, args)
}
