package smi

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"gpu-reclaimer-agent/internal/sampling"
)

type Sampler struct {
	BinaryPath string
}

func New(binaryPath string) *Sampler {
	if strings.TrimSpace(binaryPath) == "" {
		binaryPath = "nvidia-smi"
	}
	return &Sampler{BinaryPath: binaryPath}
}

func (s *Sampler) Name() string { return "nvidia-smi" }

func (s *Sampler) Close() error { return nil }

func (s *Sampler) Sample(ctx context.Context) (sampling.Snapshot, error) {
	// Query GPU summary.
	gpus, err := s.queryGPUs(ctx)
	if err != nil {
		return sampling.Snapshot{}, err
	}

	// Index GPUs by UUID for process association.
	byUUID := map[string]*sampling.GPUSnapshot{}
	for i := range gpus {
		if gpus[i].UUID != "" {
			byUUID[gpus[i].UUID] = &gpus[i]
		}
	}

	procs, err := s.queryComputeProcs(ctx)
	if err != nil {
		// nvidia-smi returns non-zero when no compute apps; treat as empty.
		if !errors.Is(err, errNoResults) {
			return sampling.Snapshot{}, err
		}
		procs = nil
	}

	for _, p := range procs {
		gpu := byUUID[p.GPUUUID]
		if gpu == nil {
			continue
		}
		gpu.ComputeProcs = append(gpu.ComputeProcs, sampling.GPUProcess{PID: p.PID, UsedBytes: p.UsedBytes})
	}

	return sampling.Snapshot{GPUs: gpus}, nil
}

type procRow struct {
	GPUUUID   string
	PID       int
	UsedBytes uint64
}

var errNoResults = errors.New("nvidia-smi no results")

func (s *Sampler) run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, s.BinaryPath, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		se := strings.TrimSpace(stderr.String())
		// Some versions print "No running processes found" on stderr and exit non-zero.
		if strings.Contains(strings.ToLower(se), "no running processes") || strings.Contains(strings.ToLower(se), "no running") {
			return nil, errNoResults
		}
		return nil, fmt.Errorf("nvidia-smi failed: %w: %s", err, se)
	}
	return out, nil
}

func (s *Sampler) queryGPUs(ctx context.Context) ([]sampling.GPUSnapshot, error) {
	qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	out, err := s.run(qctx,
		"--query-gpu=index,uuid,utilization.gpu,utilization.memory,memory.used,memory.total",
		"--format=csv,noheader,nounits",
	)
	if err != nil {
		return nil, err
	}

	lines := readCSVLines(out)
	gpus := make([]sampling.GPUSnapshot, 0, len(lines))
	for _, cols := range lines {
		if len(cols) < 6 {
			continue
		}
		idx, _ := strconv.Atoi(cols[0])
		uuid := cols[1]
		utilGPU, _ := strconv.Atoi(cols[2])
		utilMem, _ := strconv.Atoi(cols[3])
		memUsedMiB, _ := strconv.ParseUint(cols[4], 10, 64)
		memTotalMiB, _ := strconv.ParseUint(cols[5], 10, 64)

		gpus = append(gpus, sampling.GPUSnapshot{
			Index:         idx,
			UUID:          uuid,
			UtilGPU:       uint32(utilGPU),
			UtilMem:       uint32(utilMem),
			MemUsedBytes:  memUsedMiB * 1024 * 1024,
			MemTotalBytes: memTotalMiB * 1024 * 1024,
			ComputeProcs:  nil,
		})
	}
	return gpus, nil
}

func (s *Sampler) queryComputeProcs(ctx context.Context) ([]procRow, error) {
	qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	out, err := s.run(qctx,
		"--query-compute-apps=gpu_uuid,pid,used_gpu_memory",
		"--format=csv,noheader,nounits",
	)
	if err != nil {
		return nil, err
	}

	lines := readCSVLines(out)
	rows := make([]procRow, 0, len(lines))
	for _, cols := range lines {
		if len(cols) < 3 {
			continue
		}
		pid, _ := strconv.Atoi(cols[1])
		memMiB, _ := strconv.ParseUint(cols[2], 10, 64)
		rows = append(rows, procRow{GPUUUID: cols[0], PID: pid, UsedBytes: memMiB * 1024 * 1024})
	}
	return rows, nil
}

func readCSVLines(b []byte) [][]string {
	scanner := bufio.NewScanner(bytes.NewReader(b))
	out := [][]string{}
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		cols := strings.Split(line, ",")
		for i := range cols {
			cols[i] = strings.TrimSpace(cols[i])
		}
		out = append(out, cols)
	}
	return out
}
