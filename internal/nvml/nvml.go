package nvmlwrap

import (
	"context"
	"fmt"

	"github.com/NVIDIA/go-nvml/pkg/nvml"

	"gpu-reclaimer-agent/internal/sampling"
)

// Sampler implements sampling via NVML (go-nvml cgo bindings).

type Client struct {
	initialized bool
}

func New() *Client {
	return &Client{}
}

func (c *Client) Init() error {
	if c.initialized {
		return nil
	}
	ret := nvml.Init()
	if ret != nvml.SUCCESS {
		return fmt.Errorf("nvml init failed: %s", nvml.ErrorString(ret))
	}
	c.initialized = true
	return nil
}

func (c *Client) Shutdown() {
	if !c.initialized {
		return
	}
	_ = nvml.Shutdown()
	c.initialized = false
}

func (c *Client) Name() string { return "nvml" }

func (c *Client) Close() error {
	c.Shutdown()
	return nil
}

func (c *Client) Sample(ctx context.Context) (sampling.Snapshot, error) {
	_ = ctx
	if err := c.Init(); err != nil {
		return sampling.Snapshot{}, err
	}

	count, ret := nvml.DeviceGetCount()
	if ret != nvml.SUCCESS {
		return sampling.Snapshot{}, fmt.Errorf("nvml device get count failed: %s", nvml.ErrorString(ret))
	}

	snap := sampling.Snapshot{GPUs: make([]sampling.GPUSnapshot, 0, count)}
	for i := 0; i < count; i++ {
		dev, ret := nvml.DeviceGetHandleByIndex(i)
		if ret != nvml.SUCCESS {
			return sampling.Snapshot{}, fmt.Errorf("nvml get handle index=%d failed: %s", i, nvml.ErrorString(ret))
		}

		uuid, _ := dev.GetUUID()
		util, _ := dev.GetUtilizationRates()
		memInfo, _ := dev.GetMemoryInfo()

		procs, _ := dev.GetComputeRunningProcesses()
		procList := make([]sampling.GPUProcess, 0, len(procs))
		for _, p := range procs {
			procList = append(procList, sampling.GPUProcess{PID: int(p.Pid), UsedBytes: p.UsedGpuMemory})
		}

		snap.GPUs = append(snap.GPUs, sampling.GPUSnapshot{
			Index:         i,
			UUID:          uuid,
			UtilGPU:       util.Gpu,
			UtilMem:       util.Memory,
			MemUsedBytes:  memInfo.Used,
			MemTotalBytes: memInfo.Total,
			ComputeProcs:  procList,
		})
	}

	return snap, nil
}
