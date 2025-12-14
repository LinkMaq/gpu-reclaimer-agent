package idle

import "time"

type PodKey struct {
	UID         string
	Namespace   string
	Name        string
	ContainerID string
}

type PodEvidence struct {
	GPUs        []int
	PIDs        []int
	Cmdlines    []string
	UtilSamples int
	IdleSince   time.Time
}

type PodState struct {
	Key        PodKey
	IdleCount  int
	IdleSince  time.Time
	LastSeen   time.Time
	LastActive time.Time
	Reported   bool

	LastEvidence PodEvidence
}

type Tracker struct {
	IdleMinutes            int
	ConsecutiveIdleSamples int
	SampleInterval         time.Duration

	states map[string]*PodState
}

func NewTracker(idleMinutes, consecutive int, interval time.Duration) *Tracker {
	return &Tracker{
		IdleMinutes:            idleMinutes,
		ConsecutiveIdleSamples: consecutive,
		SampleInterval:         interval,
		states:                 map[string]*PodState{},
	}
}

func keyString(k PodKey) string {
	// Prefer stable UID when available, else fallback to container ID.
	if k.UID != "" {
		return "uid:" + k.UID
	}
	if k.ContainerID != "" {
		return "cid:" + k.ContainerID
	}
	return "unknown"
}

type Observation struct {
	Key      PodKey
	SeenAt   time.Time
	Idle     bool
	GPUs     []int
	PIDs     []int
	Cmdlines []string
}

type Candidate struct {
	Key      PodKey
	Evidence PodEvidence
	IdleFor  time.Duration
}

func (t *Tracker) Observe(obs Observation) (cand *Candidate) {
	ks := keyString(obs.Key)
	st := t.states[ks]
	if st == nil {
		st = &PodState{Key: obs.Key}
		t.states[ks] = st
	}
	st.LastSeen = obs.SeenAt

	if obs.Idle {
		st.IdleCount++
		if st.IdleCount == 1 {
			st.IdleSince = obs.SeenAt
		}
		st.LastEvidence = PodEvidence{
			GPUs:        append([]int(nil), obs.GPUs...),
			PIDs:        append([]int(nil), obs.PIDs...),
			Cmdlines:    append([]string(nil), obs.Cmdlines...),
			UtilSamples: st.IdleCount,
			IdleSince:   st.IdleSince,
		}
	} else {
		st.IdleCount = 0
		st.IdleSince = time.Time{}
		st.LastActive = obs.SeenAt
		st.Reported = false
		st.LastEvidence = PodEvidence{}
	}

	if !st.Reported && st.IdleCount >= t.ConsecutiveIdleSamples && !st.IdleSince.IsZero() {
		idleFor := obs.SeenAt.Sub(st.IdleSince)
		if idleFor >= time.Duration(t.IdleMinutes)*time.Minute {
			c := Candidate{Key: st.Key, Evidence: st.LastEvidence, IdleFor: idleFor}
			st.Reported = true
			return &c
		}
	}
	return nil
}

func (t *Tracker) GC(now time.Time, maxAge time.Duration) {
	for k, st := range t.states {
		if now.Sub(st.LastSeen) > maxAge {
			delete(t.states, k)
		}
	}
}
