// Package health measures how a bank is responding, from the payments an
// observer actually sends to it, and applies the same thresholds as the
// healthflag chaincode.
package health

import (
	"sort"
	"sync"
	"time"
)

// Stats summarises the payments sent to one bank over a period.
type Stats struct {
	TxCount      int
	TimeoutCount int
	FailureCount int
	P95LatencyMs int
}

// Config mirrors the healthflag chaincode's thresholds.
type Config struct {
	MinSample        int `json:"minSample"`
	TimeoutRateBps   int `json:"timeoutRateBps"`
	FailureRateBps   int `json:"failureRateBps"`
	P95LatencyMs     int `json:"p95LatencyMs"`
	FreshnessSeconds int `json:"freshnessSeconds"`
	CooldownSeconds  int `json:"cooldownSeconds"`
	ProbeShareBps    int `json:"probeShareBps"`
	ProbeMinSample   int `json:"probeMinSample"`
}

// Degraded reports whether the stats cross a threshold. It must stay identical
// to classify() in chaincode/healthflag/contract.go, or a switch will propose
// changes the chaincode then rejects.
func Degraded(s Stats, minSample int, c Config) bool {
	if s.TxCount < minSample || s.TxCount == 0 {
		return false
	}
	return s.TimeoutCount*10000/s.TxCount >= c.TimeoutRateBps ||
		s.FailureCount*10000/s.TxCount >= c.FailureRateBps ||
		s.P95LatencyMs >= c.P95LatencyMs
}

type sample struct {
	at      time.Time
	latency time.Duration
	timeout bool
	failure bool
}

// Meter records the outcome of every request an observer sends to each bank.
type Meter struct {
	mu      sync.Mutex
	samples map[string][]sample
}

func NewMeter() *Meter {
	return &Meter{samples: map[string][]sample{}}
}

// Record notes one request to bank. A timeout is not also counted as a failure.
func (m *Meter) Record(bank string, latency time.Duration, timeout, failure bool) {
	m.mu.Lock()
	m.samples[bank] = append(m.samples[bank], sample{time.Now(), latency, timeout, failure && !timeout})
	m.mu.Unlock()
}

// Since summarises the requests sent to bank at or after t.
func (m *Meter) Since(bank string, t time.Time) Stats {
	m.mu.Lock()
	defer m.mu.Unlock()
	var st Stats
	var lat []int
	for _, s := range m.samples[bank] {
		if s.at.Before(t) {
			continue
		}
		st.TxCount++
		if s.timeout {
			st.TimeoutCount++
		}
		if s.failure {
			st.FailureCount++
		}
		lat = append(lat, int(s.latency.Milliseconds()))
	}
	if len(lat) > 0 {
		sort.Ints(lat)
		st.P95LatencyMs = lat[(len(lat)*95+99)/100-1]
	}
	return st
}

// Prune drops samples older than t so memory stays bounded.
func (m *Meter) Prune(t time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for bank, ss := range m.samples {
		i := 0
		for i < len(ss) && ss[i].at.Before(t) {
			i++
		}
		if i > 0 {
			m.samples[bank] = append([]sample(nil), ss[i:]...)
		}
	}
}
