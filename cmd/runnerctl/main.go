package main

import (
	"context"
	"os"
	"os/signal"

	"git.ser60v.com/retardsoftware/runner-platform/cli/internal/cli"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(cli.New(os.Stdin, os.Stdout, os.Stderr, version).Run(ctx, os.Args[1:]))
}
