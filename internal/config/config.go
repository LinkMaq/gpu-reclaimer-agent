package config

import (
	"flag"
	"os"
	"strconv"
	"time"
)

type Config struct {
	IdleMinutes            int
	SampleInterval         time.Duration
	ConsecutiveIdleSamples int
	GPUUtilThresholdPct    int
	TermGraceSeconds       int
	MaxReclaimRetry        int
	DryRun                 bool

	CRIEndpoint string

	// If set, only nodes with this label key/value are enabled. (M1 prototype: not enforced)
	NodeSelectorLabel string

	// Pod-level opt-out annotation key. If present and equals "false", never reclaim.
	PodEnabledAnnotationKey string
	PodEnabledDefault       bool

	ProcessAllowlistRegex string
}

func FromEnvAndFlags(args []string) Config {
	fs := flag.NewFlagSet("gpu-reclaimer-agent", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	cfg := Config{
		IdleMinutes:            envInt("IDLE_MINUTES", 30),
		SampleInterval:         time.Duration(envInt("SAMPLE_INTERVAL_SECONDS", 60)) * time.Second,
		ConsecutiveIdleSamples: envInt("CONSECUTIVE_IDLE_SAMPLES", 30),
		GPUUtilThresholdPct:    envInt("GPU_UTIL_THRESHOLD_PERCENT", 1),
		TermGraceSeconds:       envInt("TERM_GRACE_SECONDS", 15),
		MaxReclaimRetry:        envInt("MAX_RECLAIM_RETRY", 2),
		DryRun:                 envBool("DRY_RUN", false),
		CRIEndpoint:            os.Getenv("CRI_ENDPOINT"),
		PodEnabledAnnotationKey: envString("POD_ENABLED_ANNOTATION_KEY", "gpu-reclaimer/enabled"),
		PodEnabledDefault:       envBool("POD_ENABLED_DEFAULT", true),
		ProcessAllowlistRegex: envString("PROCESS_ALLOWLIST_REGEX", "(^|/)(nvidia-persistenced|nvidia-powerd)$"),
	}

	fs.IntVar(&cfg.IdleMinutes, "idle-minutes", cfg.IdleMinutes, "Idle threshold in minutes")
	fs.DurationVar(&cfg.SampleInterval, "sample-interval", cfg.SampleInterval, "Sampling interval")
	fs.IntVar(&cfg.ConsecutiveIdleSamples, "consecutive-idle-samples", cfg.ConsecutiveIdleSamples, "Consecutive idle samples needed")
	fs.IntVar(&cfg.GPUUtilThresholdPct, "gpu-util-threshold", cfg.GPUUtilThresholdPct, "GPU util threshold percent (util < threshold is idle)")
	fs.BoolVar(&cfg.DryRun, "dry-run", cfg.DryRun, "Dry-run mode (no signals)")
	fs.StringVar(&cfg.CRIEndpoint, "cri-endpoint", cfg.CRIEndpoint, "CRI runtime endpoint for crictl (optional)")
	fs.StringVar(&cfg.PodEnabledAnnotationKey, "pod-enabled-annotation", cfg.PodEnabledAnnotationKey, "Pod annotation key used to enable/disable reclaim")
	fs.BoolVar(&cfg.PodEnabledDefault, "pod-enabled-default", cfg.PodEnabledDefault, "Default pod enabled when annotation is absent")
	fs.StringVar(&cfg.ProcessAllowlistRegex, "process-allowlist-regex", cfg.ProcessAllowlistRegex, "Regex for processes to never reclaim")
	_ = fs.Parse(args)

	return cfg
}

func envString(key, def string) string {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	return v
}

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return i
}

func envBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}
