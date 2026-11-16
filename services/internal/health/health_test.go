package health

import (
	"testing"
	"time"
)

func TestDegradedMatchesChaincodeThresholds(t *testing.T) {
	cfg := Config{MinSample: 20, TimeoutRateBps: 500, FailureRateBps: 1000, P95LatencyMs: 3000}
	cases := []struct {
		name string
		s    Stats
		want bool
	}{
		{"clean", Stats{TxCount: 100, P95LatencyMs: 200}, false},
		{"too few to judge", Stats{TxCount: 5, TimeoutCount: 5}, false},
		{"empty", Stats{}, false},
		{"timeouts at threshold", Stats{TxCount: 100, TimeoutCount: 5}, true},
		{"timeouts just under", Stats{TxCount: 1000, TimeoutCount: 49}, false},
		{"failures at threshold", Stats{TxCount: 100, FailureCount: 10}, true},
		{"slow", Stats{TxCount: 100, P95LatencyMs: 3000}, true},
	}
	for _, c := range cases {
		if got := Degraded(c.s, cfg.MinSample, cfg); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestMeterWindowAndP95(t *testing.T) {
	m := NewMeter()
	before := time.Now()
	for i := 1; i <= 100; i++ {
		m.Record("BANKB", time.Duration(i)*time.Millisecond, i > 95, i == 1)
	}
	m.Record("BANKA", time.Second, false, false)

	st := m.Since("BANKB", before)
	if st.TxCount != 100 || st.TimeoutCount != 5 || st.FailureCount != 1 {
		t.Fatalf("unexpected counts: %+v", st)
	}
	if st.P95LatencyMs != 95 {
		t.Fatalf("p95 of 1..100ms should be 95, got %d", st.P95LatencyMs)
	}
	if got := m.Since("BANKB", time.Now().Add(time.Second)); got.TxCount != 0 {
		t.Fatalf("a window starting in the future should be empty, got %+v", got)
	}
	m.Prune(time.Now().Add(time.Second))
	if got := m.Since("BANKA", before); got.TxCount != 0 {
		t.Fatalf("prune should drop old samples, got %+v", got)
	}
}

func TestTimeoutIsNotAlsoAFailure(t *testing.T) {
	m := NewMeter()
	before := time.Now()
	m.Record("BANKB", time.Second, true, true)
	if st := m.Since("BANKB", before); st.TimeoutCount != 1 || st.FailureCount != 0 {
		t.Fatalf("a timeout must count once: %+v", st)
	}
}
