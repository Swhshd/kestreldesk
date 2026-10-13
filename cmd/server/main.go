package main

// This file has been modified for the KestrelDesk project.

import (
	"context"
	"flag"
	"log/slog"
	"os"

	"github.com/Swhshd/kestreldesk/internal/bootstrap"
	"github.com/Swhshd/kestreldesk/internal/operatorcli"
	"github.com/Swhshd/kestreldesk/internal/pkg/config"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "admin" {
		os.Exit(operatorcli.Run(context.Background(), os.Args[2:], os.Stdin, os.Stdout, os.Stderr, operatorcli.SystemDependencies()))
	}

	configPath := flag.String("config", "config/config.yaml", "path to config file")
	flag.Parse()

	if err := bootstrap.Init(*configPath); err != nil {
		slog.Error("bootstrap init failed", "error", err)
		os.Exit(1)
	}

	cfg := config.Current()

	app, err := bootstrap.NewServer()
	if err != nil {
		slog.Error("bootstrap server failed", "error", err)
		return
	}

	if err := app.Run(cfg.Server.Address()); err != nil {
		slog.Error("start server failed", "error", err)
		return
	}
}
