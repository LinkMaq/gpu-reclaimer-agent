package agent

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"gpu-reclaimer-agent/internal/attribution"
	"gpu-reclaimer-agent/internal/config"
	"gpu-reclaimer-agent/internal/idle"
	"gpu-reclaimer-agent/internal/logging"
	"gpu-reclaimer-agent/internal/sampling"
	"gpu-reclaimer-agent/internal/smi"
	nvmlwrap "gpu-reclaimer-agent/internal/nvml"
)

type Options struct {
	Config   config.Config
	NodeName string
	Logger   *logging.Logger
}

type Agent struct {
	cfg     config.Config
	node    string
	log     *logging.Logger
	sampler sampling.Sampler
	attrib  *attribution.Resolver
	tracker *idle.Tracker

	allowlist *regexp.Regexp
}

func New(opts Options) *Agent {
	allow := regexp.MustCompile(opts.Config.ProcessAllowlistRegex)
	var sampler sampling.Sampler
	switch strings.ToLower(strings.TrimSpace(opts.Config.Sampler)) {
	case "smi", "nvidia-smi", "nvidiasmi":
		sampler = smi.New("nvidia-smi")
	default:
		sampler = nvmlwrap.New()
	}
	return &Agent{
		cfg:       opts.Config,
		node:      opts.NodeName,
		log:       opts.Logger,
		sampler:   sampler,
		attrib:    attribution.NewResolver(opts.Config.CRIEndpoint),
		tracker:   idle.NewTracker(opts.Config.IdleMinutes, opts.Config.ConsecutiveIdleSamples, opts.Config.SampleInterval),
		allowlist: allow,
	}
}

func (a *Agent) Run(ctx context.Context) error {
	ticker := time.NewTicker(a.cfg.SampleInterval)
	defer ticker.Stop()
	defer func() { _ = a.sampler.Close() }()

	a.log.Info(map[string]any{"msg": "gpu sampler selected", "node": a.node, "sampler": a.sampler.Name()})

	// First sample immediately.
	if err := a.tick(ctx); err != nil {
		a.log.Warn(map[string]any{"msg": "initial tick failed", "error": err.Error()})
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := a.tick(ctx); err != nil {
				a.log.Warn(map[string]any{"msg": "tick failed", "error": err.Error()})
			}
		}
	}
}

type podAgg struct {
	key      idle.PodKey
	gpusSet  map[int]struct{}
	pidsSet  map[int]struct{}
	cmdlines []string

	gpuIdle map[int]bool
}

func (a *Agent) tick(ctx context.Context) error {
	snap, err := a.sampler.Sample(ctx)
	if err != nil {
		return err
	}

	now := time.Now()
	pods := map[string]*podAgg{}
	attribFail := 0

	for _, g := range snap.GPUs {
		gpuIsIdle := int(g.UtilGPU) < a.cfg.GPUUtilThresholdPct
		for _, p := range g.ComputeProcs {
			// Best-effort attribution; if we can't attribute, we won't act.
			pid := p.PID
			attrCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			attr, err := a.attrib.ResolvePID(attrCtx, pid)
			cancel()
			if err != nil {
				attribFail++
				a.log.Warn(map[string]any{
					"msg":   "pid attribution failed",
					"node":  a.node,
					"gpu":   g.Index,
					"pid":   pid,
					"error": err.Error(),
				})
				continue
			}

			if attr.Cmdline != "" && a.allowlist.MatchString(attr.Cmdline) {
				// Never consider system allowlisted processes.
				continue
			}

			k := idle.PodKey{UID: attr.PodUID, Namespace: attr.PodNamespace, Name: attr.PodName, ContainerID: attr.ContainerID}
			ks := podKeyString(k)
			agg := pods[ks]
			if agg == nil {
				agg = &podAgg{
					key:     k,
					gpusSet:  map[int]struct{}{},
					pidsSet:  map[int]struct{}{},
					gpuIdle:  map[int]bool{},
					cmdlines: nil,
				}
				pods[ks] = agg
			}
			agg.gpusSet[g.Index] = struct{}{}
			agg.pidsSet[pid] = struct{}{}
			if attr.Cmdline != "" {
				agg.cmdlines = append(agg.cmdlines, attr.Cmdline)
			}
			// If the pod touches this GPU, its idleness depends on this GPU.
			agg.gpuIdle[g.Index] = gpuIsIdle
		}
	}

	if attribFail > 0 {
		a.log.Info(map[string]any{"msg": "pid attribution failures in tick", "node": a.node, "count": attribFail})
	}

	for _, agg := range pods {
		pids := setToSortedInts(agg.pidsSet)
		gpus := setToSortedInts(agg.gpusSet)
		idleNow := true
		for _, gi := range gpus {
			if ok, exists := agg.gpuIdle[gi]; !exists || !ok {
				idleNow = false
				break
			}
		}

		cand := a.tracker.Observe(idle.Observation{
			Key:      agg.key,
			SeenAt:   now,
			Idle:     idleNow,
			GPUs:     gpus,
			PIDs:     pids,
			Cmdlines: limitStrings(agg.cmdlines, 5),
		})

		if cand == nil {
			continue
		}

		// FR-4: immediate validation to avoid edge mis-kill.
		valid, reason, vErr := a.validateCandidate(ctx, *cand)
		if vErr != nil {
			a.log.Warn(map[string]any{"msg": "candidate validation error", "node": a.node, "error": vErr.Error()})
			continue
		}
		if !valid {
			a.log.Info(map[string]any{"msg": "candidate no longer valid", "node": a.node, "reason": reason, "pod_uid": cand.Key.UID, "container_id": cand.Key.ContainerID})
			continue
		}

		// M1: dry-run only (no signals).
		action := "dry_run"
		if !a.cfg.DryRun {
			action = "dry_run_enforced_m1"
		}
		a.log.Info(map[string]any{
			"msg":          "reclaim candidate (dry-run)",
			"node":         a.node,
			"action":       action,
			"dry_run_cfg":  a.cfg.DryRun,
			"idle_minutes": int(cand.IdleFor.Minutes()),
			"util_samples": cand.Evidence.UtilSamples,
			"gpu_indexes":  gpus,
			"pids":         pids,
			"cmdlines":     cand.Evidence.Cmdlines,
			"pod_uid":      cand.Key.UID,
			"pod_ns":       cand.Key.Namespace,
			"pod_name":     cand.Key.Name,
			"container_id": cand.Key.ContainerID,
		})
	}

	// Keep state bounded.
	a.tracker.GC(now, 2*time.Hour)
	return nil
}

func (a *Agent) validateCandidate(ctx context.Context, cand idle.Candidate) (bool, string, error) {
	snap, err := a.sampler.Sample(ctx)
	if err != nil {
		return false, "resample_failed", err
	}
	gpuIdxSet := map[int]struct{}{}
	for _, gi := range cand.Evidence.GPUs {
		gpuIdxSet[gi] = struct{}{}
	}
	pidSet := map[int]struct{}{}
	for _, pid := range cand.Evidence.PIDs {
		pidSet[pid] = struct{}{}
	}

	seenAnyPID := false
	for _, g := range snap.GPUs {
		if _, ok := gpuIdxSet[g.Index]; !ok {
			continue
		}
		if int(g.UtilGPU) >= a.cfg.GPUUtilThresholdPct {
			return false, fmt.Sprintf("gpu_%d_util_not_idle", g.Index), nil
		}
		for _, p := range g.ComputeProcs {
			if _, ok := pidSet[p.PID]; ok {
				seenAnyPID = true
				break
			}
		}
	}

	if !seenAnyPID {
		return false, "pids_gone", nil
	}
	return true, "ok", nil
}

func podKeyString(k idle.PodKey) string {
	if k.UID != "" {
		return "uid:" + k.UID
	}
	if k.ContainerID != "" {
		return "cid:" + k.ContainerID
	}
	return "unknown"
}

func setToSortedInts(m map[int]struct{}) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

func limitStrings(in []string, max int) []string {
	if len(in) <= max {
		return in
	}
	return in[:max]
}
