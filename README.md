# gpu-reclaimer-agent（M1 原型）

本仓库实现 PRD v1.0 的 **M1：NVML 采样 + PID 归因 + dry-run**。

## 能做什么（当前阶段）

- 周期性读取 NVML：每张 GPU 的利用率、显存、compute 进程 PID 列表

> 说明：在少数环境里，NVML cgo 调用可能触发 `*** stack smashing detected ***`（通常与 driver/library 或 ABI 组合相关）。
> 为了让原型先跑起来，本仓库支持采样后端切换为 `nvidia-smi`。
- 将 NVML PID 归因到 Pod/容器（best-effort）：
  - 解析 `/proc/<pid>/cgroup` 提取 `podUID`、`containerID`
  - 若容器内存在 `crictl` 且可访问 CRI socket，则用 `crictl inspect` 补全 `namespace/name/containerName`
- 基于“连续采样 + GPU util < 阈值”判定空闲候选，并 **仅输出 dry-run 日志**（不发送信号）

## 构建

```bash
go mod tidy
go build ./cmd/gpu-reclaimer-agent
```

## 本地运行（需要 NVIDIA 驱动 + 可用 NVML）

```bash
./gpu-reclaimer-agent \
  --dry-run=true \
  --idle-minutes=30 \
  --sample-interval=60s \
  --consecutive-idle-samples=30 \
  --gpu-util-threshold=1
```

## 重要前提（DaemonSet 上线前请确认）

- 进程归因依赖读取宿主机 `/proc/<pid>`，通常需要 `hostPID: true`
- NVML 访问依赖宿主机 NVIDIA 驱动暴露 `libnvidia-ml.so` 与 `/dev/nvidia*`
- 若希望补全 `pod ns/name/container`，需要容器内可用 `crictl` 并挂载 CRI socket（如 containerd：`/run/containerd/containerd.sock`）

## 配置项（env/flag）

- `IDLE_MINUTES` / `--idle-minutes`（默认 30）
- `SAMPLE_INTERVAL_SECONDS` / `--sample-interval`（默认 60s）
- `CONSECUTIVE_IDLE_SAMPLES` / `--consecutive-idle-samples`（默认 30）
- `GPU_UTIL_THRESHOLD_PERCENT` / `--gpu-util-threshold`（默认 1）
- `DRY_RUN` / `--dry-run`（默认 false；M1 阶段即使 false 也只会 dry-run）
- `SAMPLER` / `--sampler`（默认 `nvml`；可选 `smi`）
- `CRI_ENDPOINT` / `--cri-endpoint`（可选，供 `crictl -r` 使用）
- `PROCESS_ALLOWLIST_REGEX`（默认忽略 `nvidia-persistenced` 等）
