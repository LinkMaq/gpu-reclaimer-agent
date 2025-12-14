package sampling

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
