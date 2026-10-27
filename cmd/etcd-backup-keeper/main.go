package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"etcd-backup-keeper/internal/backup"
	"etcd-backup-keeper/internal/cli"
)

// Set with -ldflags '-X main.version=...' when building a release.
var version = "dev"

func main() {
	if handled, code := backup.InspectionHelper(os.Args[1:]); handled {
		os.Exit(code)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Run(ctx, os.Args[1:], os.Stdout, os.Stderr, version)
	stop()
	os.Exit(code)
}
