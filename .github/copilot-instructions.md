PRD：NVIDIA GPU 空闲回收与复用（方案 B：空闲即杀 GPU 进程）
版本：v1.0
日期：2025-12-14

## 1. 背景与问题
- 在 AI 开发调试场景中，Pod/用户进程会长期驻留并可能持有 CUDA context/显存，即使无实际调用，也会阻塞同节点其它 Pod 的有效 GPU 使用。
- 期望实现：当 Pod 在 30 分钟内无 GPU 调用/无有效 GPU 活动时，自动回收其 GPU 占用；回收后 GPU 可被其它 Pod 使用；Pod 本体不停止（允许 GPU 相关进程被回收时中断）。

## 2. 目标（Goals）
- G1：仅针对 NVIDIA GPU，实现“30 分钟空闲自动回收 GPU 占用”，并确保回收后其它 Pod 可复用（在 GPU 共享调度前提下）。
- G2：Pod 生命周期不受影响（不删除、不驱逐、不重建 Pod）；回收动作仅作用于 GPU 相关进程。
- G3：支持开发调试负载，提供可配置的回收策略（宽松/激进）。
- G4：可观测、可审计：每次回收必须可追踪到 Pod/容器/进程、原因与证据（NVML 指标、空闲时长等）。
## 3. 非目标（Non-Goals）
- NG1：不保证“回收过程对用户进程完全无感”。本方案允许 kill 占用 GPU 的进程（例如 Jupyter kernel、Python REPL、推理进程），可能导致任务中断或报错。
- NG2：不实现 kubelet/device-plugin 级别的运行中 Deallocate/热插拔扩展资源（Kubernetes 原生不支持）。
- NG3：不做跨节点迁移、会话保持、进程状态恢复或模型热迁移。
- NG4：不承诺对所有 CUDA 框架“卸载后自动恢复到完全一致状态”（kill 后仅能重新启动/重新加载）。
## 4. 用户与使用场景
- S1（开发调试）：用户长期开着 Jupyter/IDE 容器，偶尔跑 GPU；空闲时希望平台自动让出；再次使用时允许重新加载/重新启动 GPU 进程。
## 5. 产品形态（交付物）
- 节点级守护进程（DaemonSet）：gpu-reclaimer-agent
- 配套配置：ConfigMap/参数（阈值、判定策略、白名单、信号、超时、灰度、dry-run）
- 可观测性：Prometheus 指标 + 结构化日志 +（可选）Kubernetes Events
- 运维控制：namespace/pod 级开关（label/annotation），支持灰度与 dry-run
## 6. 总体方案概述
- 集群启用 NVIDIA device plugin 的 GPU sharing（time-slicing）能力：将每张物理 GPU 以 N 份逻辑 slot 形式暴露，允许多个 Pod 调度到同一张卡。
- 节点侧 gpu-reclaimer-agent 周期性从 NVML 获取：
  - GPU 上 compute 进程 PID 列表
  - GPU 利用率 / 显存占用（按 GPU）
- agent 将 GPU PID 归因到 Pod/容器（通过 /proc/<pid>/cgroup + CRI 元数据），识别“属于某 Pod 的 GPU 进程集合”。
- 当判定某 Pod 在连续 30 分钟内无有效 GPU 活动且满足安全条件时，对其 GPU 进程执行回收：
  - 先 SIGTERM 优雅退出
  - 超时仍存在则 SIGKILL 兜底
- 回收后 GPU context/显存释放，GPU 对其它 Pod 立即可用（slot + 运行时占用双重释放）。
## 7. 功能需求（Functional Requirements）
### 7.1 空闲检测
- FR-1：空闲阈值默认 30 分钟，可配置（分钟）。
- FR-2：采样间隔可配置（默认 60s）。
- FR-3：空闲判定采用连续采样满足条件的方式（抖动保护）：
  - 连续 N 次采样满足“利用率低于阈值”（默认阈值 <1%）
  - 并且该 Pod 归因的 GPU 进程在该窗口内无显著活动（v1 可用“全卡利用率 + 该 Pod PID 是否存在”组合实现；后续可增强到 per-process 指标或更精细的活动判定）
- FR-4：进入回收前需进行一次“即时校验”（再次读取 NVML），避免边界误判。
### 7.2 PID → Pod/容器归因
- FR-5：对 NVML 返回的每个 GPU PID：
  - 读取 /proc/<pid>/cgroup（兼容 cgroup v1/v2）
  - 映射到 container ID（containerd/cri-o）
  - 得到 pod UID / namespace / name / container name
- FR-6：归因失败处理：
  - 记录日志与指标
  - 默认不对该 PID 执行回收（安全优先）；仅当归因明确属于目标 Pod 才回收
### 7.3 回收执行（信号与超时）
- FR-7：回收动作按 Pod 维度执行：回收该 Pod 归因到的 GPU 进程集合。
- FR-8：信号策略可配置，默认：
  - SIGTERM → 等待 TERM_GRACE_SECONDS（默认 15s）
  - 仍存在则 SIGKILL
- FR-9：回收后验证：
  - 再次查询 NVML，确认相关 PID 不再出现在 GPU 进程列表中
  - 回收失败需记录原因、重试计数（默认最多重试 2 次）
### 7.4 策略控制（开关/白名单）
- FR-10：支持至少两级开关：
  - 全局开关（DaemonSet 参数）
  - Pod 级开关（annotation），如 gpu-reclaimer/enabled: "true|false"
- FR-11：支持白名单（不回收）：
  - 进程名/命令行正则（默认包含系统 NVIDIA 常驻进程，如 nvidia-persistenced 等）
  - namespace/Pod label 白名单（系统组件、关键在线服务）
- FR-12：支持 dry-run：
  - 仅输出“将要回收”的 pod/pid/证据，不发送信号
### 7.5 灰度发布
- FR-13：支持按节点 label 选择性启用（灰度节点池）。
- FR-14：支持按 namespace 分级策略（开发调试更宽松，推理更激进）。
## 8. 非功能需求（Non-Functional Requirements）
- NFR-1：agent 崩溃不影响 GPU 正常使用；恢复后继续观测。
- NFR-2：agent 的 CPU/内存开销可控（目标：常驻低开销），采样不应显著影响 GPU 性能。
- NFR-3：安全与审计：
  - kill 行为必须可审计（PID、Pod、容器、信号、原因、证据、时间）
  - 权限最小化（如需 hostPID/特权，必须在文档中明确并提供风险说明）
- NFR-4：可观测：指标与日志能够快速定位误杀、归因失败、回收失败。
## 9. 技术实现原理（Why it works）
- NVIDIA GPU 的“占用”本质由进程持有的 CUDA context / driver 句柄 / 显存分配决定。
- 进程退出后，driver 回收其 context 与显存；NVML/nvidia-smi 中该进程条目消失，GPU 回到可被其它进程使用的状态。
- 共享调度（time-slicing）解决“调度层可复用”的前提；本方案解决“运行时占用被空闲进程锁死”的问题。
## 10. 风险与对策
- R1：误杀（边界时刻用户正要用 GPU）
  - 对策：连续采样 + 即时校验；先 TERM 再 KILL；支持 opt-out；支持更宽松阈值。
- R2：开发调试体验差（Jupyter kernel 被杀）
  - 对策：对 dev namespace 使用更宽松策略；回收前发 Event（可选）；加强白名单/标签豁免。
- R3：PID 归因不准导致误操作
  - 对策：归因不明确一律不动；归因失败指标告警；逐步覆盖不同 runtime/cgroup 形态。
- R4：权限过大带来安全隐患
  - 对策：最小权限、审计日志、灰度启用；明确安全评审项。
- R5：共享并发带来的性能抖动
  - 对策：合理设置 replicas；按 namespace 配额与分级策略控制并发；上线前压测。
## 11. 配置项（建议默认值）
- IDLE_MINUTES=30
- SAMPLE_INTERVAL_SECONDS=60
- CONSECUTIVE_IDLE_SAMPLES=30
- GPU_UTIL_THRESHOLD_PERCENT=1
- TERM_GRACE_SECONDS=15
- MAX_RECLAIM_RETRY=2
- DRY_RUN=false
- POD_OPT_OUT_ANNOTATION=gpu-reclaimer/enabled=false
- PROCESS_ALLOWLIST_REGEX（默认包含系统必要进程）
## 12. 可观测性（Metrics/Logs）
- 指标（Prometheus）：
  - gpu_reclaimer_reclaim_total{result="success|fail",reason="idle"}
  - gpu_reclaimer_kill_total{signal="TERM|KILL"}
  - gpu_reclaimer_pid_attribution_fail_total
  - gpu_reclaimer_idle_candidates
- 日志（结构化）字段建议：
  - node, gpu_index, pid, cmdline, pod_ns, pod_name, pod_uid, container_id, idle_minutes, util_samples, action, result, error
- K8s Event（可选）：
  - 对被回收 Pod 发出 Warning：说明回收原因与时间
## 13. 验收标准（Acceptance Criteria）
- AC-1：制造“占用显存但无有效调用”的 Pod，达到 30 分钟阈值后 GPU 进程被回收，显存释放，NVML 中进程消失。
- AC-2：同节点上其它 Pod 在回收后能成功进行 GPU 推理/torch.cuda 初始化。
- AC-3：Pod 不重启、不被驱逐；仅 GPU 相关进程受影响。
- AC-4：dry-run 不发生 kill，输出候选对象与证据准确。
- AC-5：opt-out 的 Pod 永不被回收。
## 14. 里程碑（建议）
- M1：NVML 采样 + PID 归因 + dry-run（原型）
- M2：TERM/KILL 回收 + 指标 + 灰度开关
- M3：namespace 分级策略 + 白名单 + 误杀防护增强
- M4：压测与上线（灰度节点池 → 扩面）