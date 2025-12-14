package attribution

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Attribution struct {
	PID int

	PodUID        string
	PodNamespace  string
	PodName       string
	ContainerName string
	ContainerID   string

	Cmdline string

	Source string // cgroup|crictl|partial
}

type Resolver struct {
	criEndpoint string
	crictlPath  string
	cache       *ttlCache
}

func NewResolver(criEndpoint string) *Resolver {
	return &Resolver{
		criEndpoint: criEndpoint,
		crictlPath:  findCrictl(),
		cache:       newTTLCache(10 * time.Minute),
	}
}

func (r *Resolver) ResolvePID(ctx context.Context, pid int) (Attribution, error) {
	attr := Attribution{PID: pid}

	cmdline, _ := readCmdline(pid)
	attr.Cmdline = cmdline

	cg, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	if err != nil {
		return Attribution{}, err
	}

	podUID, containerID := parseCgroup(string(cg))
	attr.PodUID = podUID
	attr.ContainerID = containerID
	attr.Source = "cgroup"

	// Best-effort enrichment via crictl inspect.
	if containerID != "" {
		if meta, ok := r.cache.Get(containerID); ok {
			mergeMeta(&attr, meta)
			if attr.PodNamespace != "" || attr.PodName != "" {
				attr.Source = "crictl(cache)"
			}
			return attr, nil
		}

		meta, err := r.inspectContainer(ctx, containerID)
		if err == nil {
			r.cache.Set(containerID, meta)
			mergeMeta(&attr, meta)
			if attr.PodNamespace != "" || attr.PodName != "" {
				attr.Source = "crictl"
			} else {
				attr.Source = "partial"
			}
			return attr, nil
		}
	}

	if attr.PodUID == "" && attr.ContainerID == "" {
		return Attribution{}, errors.New("no k8s identifiers found in cgroup")
	}
	attr.Source = "partial"
	return attr, nil
}

type crictlMeta struct {
	PodUID        string
	PodNamespace  string
	PodName       string
	ContainerName string
	ContainerID   string
}

func mergeMeta(dst *Attribution, meta crictlMeta) {
	if dst.PodUID == "" {
		dst.PodUID = meta.PodUID
	}
	if dst.ContainerID == "" {
		dst.ContainerID = meta.ContainerID
	}
	if dst.PodNamespace == "" {
		dst.PodNamespace = meta.PodNamespace
	}
	if dst.PodName == "" {
		dst.PodName = meta.PodName
	}
	if dst.ContainerName == "" {
		dst.ContainerName = meta.ContainerName
	}
}

func (r *Resolver) inspectContainer(ctx context.Context, containerID string) (crictlMeta, error) {
	if r.crictlPath == "" {
		return crictlMeta{}, errors.New("crictl not found")
	}
	args := []string{"inspect", "--output", "json"}
	if r.criEndpoint != "" {
		args = append([]string{"-r", r.criEndpoint}, args...)
	}
	args = append(args, containerID)

	cmd := exec.CommandContext(ctx, r.crictlPath, args...)
	out, err := cmd.Output()
	if err != nil {
		return crictlMeta{}, fmt.Errorf("crictl inspect failed: %w", err)
	}

	// crictl outputs a few different JSON shapes across versions.
	// We only need labels.
	type labelsShape struct {
		Info struct {
			Config struct {
				Labels map[string]string `json:"labels"`
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
			} `json:"config"`
		} `json:"info"`
		Status struct {
			Labels map[string]string `json:"labels"`
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		} `json:"status"`
	}

	var obj labelsShape
	if err := json.Unmarshal(out, &obj); err != nil {
		return crictlMeta{}, err
	}

	labels := map[string]string{}
	for k, v := range obj.Info.Config.Labels {
		labels[k] = v
	}
	for k, v := range obj.Status.Labels {
		labels[k] = v
	}

	m := crictlMeta{
		ContainerID: containerID,
	}
	// Common label keys
	m.PodName = firstNonEmpty(labels["io.kubernetes.pod.name"], labels["io.kubernetes.pod.name"])
	m.PodNamespace = firstNonEmpty(labels["io.kubernetes.pod.namespace"], labels["io.kubernetes.pod.namespace"])
	m.ContainerName = firstNonEmpty(labels["io.kubernetes.container.name"], obj.Info.Config.Metadata.Name, obj.Status.Metadata.Name)
	m.PodUID = firstNonEmpty(labels["io.kubernetes.pod.uid"], labels["io.kubernetes.pod.uid"])

	return m, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

var (
	podUIDRe = regexp.MustCompile(`pod([0-9a-fA-F]{8}[-_][0-9a-fA-F]{4}[-_][0-9a-fA-F]{4}[-_][0-9a-fA-F]{4}[-_][0-9a-fA-F]{12})`)
	cidRe    = regexp.MustCompile(`(?:^|/)(?:docker-|crio-|cri-containerd-|containerd-)([0-9a-fA-F]{12,64})(?:\.scope)?(?:$|/)`)
)

func parseCgroup(cgroup string) (podUID string, containerID string) {
	scanner := bufio.NewScanner(strings.NewReader(cgroup))
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		path := parts[2]
		if podUID == "" {
			if m := podUIDRe.FindStringSubmatch(path); len(m) == 2 {
				podUID = strings.ReplaceAll(m[1], "_", "-")
			}
		}
		if containerID == "" {
			if m := cidRe.FindStringSubmatch(path); len(m) == 2 {
				containerID = strings.ToLower(m[1])
			}
		}
		if podUID != "" && containerID != "" {
			break
		}
	}
	return podUID, containerID
}

func readCmdline(pid int) (string, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return "", err
	}
	b = bytes.TrimRight(b, "\x00")
	parts := bytes.Split(b, []byte{0})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		s := strings.TrimSpace(string(p))
		if s != "" {
			out = append(out, s)
		}
	}
	return strings.Join(out, " "), nil
}

func findCrictl() string {
	// Prefer PATH lookup.
	if p, err := exec.LookPath("crictl"); err == nil {
		return p
	}
	// Common fallback locations.
	candidates := []string{"/usr/bin/crictl", "/usr/local/bin/crictl"}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

// ---- tiny TTL cache ----

type ttlCache struct {
	mu  sync.Mutex
	ttl time.Duration
	m   map[string]cacheItem
}

type cacheItem struct {
	val     crictlMeta
	expires time.Time
}

func newTTLCache(ttl time.Duration) *ttlCache {
	return &ttlCache{ttl: ttl, m: map[string]cacheItem{}}
}

func (c *ttlCache) Get(key string) (crictlMeta, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	it, ok := c.m[key]
	if !ok {
		return crictlMeta{}, false
	}
	if time.Now().After(it.expires) {
		delete(c.m, key)
		return crictlMeta{}, false
	}
	return it.val, true
}

func (c *ttlCache) Set(key string, val crictlMeta) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[key] = cacheItem{val: val, expires: time.Now().Add(c.ttl)}
}

// For tests/debugging convenience.
func NormalizePath(p string) string {
	return filepath.Clean(p)
}
