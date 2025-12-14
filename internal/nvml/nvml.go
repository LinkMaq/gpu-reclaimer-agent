package nvmlwrap

import (
	"fmt"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
)

type GPUProcess struct {
	PID       int
	UsedBytes uint64
}

type GPUSnapshot struct {
	Index         int
	UUID          string
	UtilGPU       uint32
	UtilMem       uint32
	MemUsedBytes  uint64
	MemTotalBytes uint64
	ComputeProcs  []GPUProcess
}

type Snapshot struct {
	GPUs []GPUSnapshot
}

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

func (c *Client) Sample() (Snapshot, error) {
	if err := c.Init(); err != nil {
		return Snapshot{}, err
	}

	count, ret := nvml.DeviceGetCount()
	if ret != nvml.SUCCESS {
		return Snapshot{}, fmt.Errorf("nvml device get count failed: %s", nvml.ErrorString(ret))
	}

	snap := Snapshot{GPUs: make([]GPUSnapshot, 0, count)}
	for i := 0; i < count; i++ {
		dev, ret := nvml.DeviceGetHandleByIndex(i)
		if ret != nvml.SUCCESS {
			return Snapshot{}, fmt.Errorf("nvml get handle index=%d failed: %s", i, nvml.ErrorString(ret))
		}

		uuid, _ := dev.GetUUID()
		util, _ := dev.GetUtilizationRates()
		memInfo, _ := dev.GetMemoryInfo()

		procs, _ := dev.GetComputeRunningProcesses()
		procList := make([]GPUProcess, 0, len(procs))
		for _, p := range procs {
			procList = append(procList, GPUProcess{PID: int(p.Pid), UsedBytes: p.UsedGpuMemory})
		}

		snap.GPUs = append(snap.GPUs, GPUSnapshot{
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
