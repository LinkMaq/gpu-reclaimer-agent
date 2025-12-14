package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gpu-reclaimer-agent/internal/agent"
	"gpu-reclaimer-agent/internal/config"
	"gpu-reclaimer-agent/internal/logging"
)

func main() {
	cfg := config.FromEnvAndFlags(os.Args[1:])
	logger := logging.NewJSONLogger(os.Stdout)

	hostname, _ := os.Hostname()
	ag := agent.New(agent.Options{
		Config:   cfg,
		NodeName: hostname,
		Logger:   logger,
	})

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	logger.Info(map[string]any{
		"msg":       "gpu-reclaimer-agent starting",
		"node":      hostname,
		"dry_run":   cfg.DryRun,
		"interval_s": int(cfg.SampleInterval.Seconds()),
	})

	if err := ag.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error(map[string]any{"msg": "gpu-reclaimer-agent exited with error", "error": err.Error()})
		// give log collector a chance
		time.Sleep(250 * time.Millisecond)
		os.Exit(1)
	}
}
