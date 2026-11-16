// Command switch is a payment switch that routes on shared bank health flags.
//
// Before any money moves it checks the payee bank's flag in a local cache. The
// cache is refreshed from ledger commit events, so the decision itself never
// waits on the ledger. A payment to a degraded bank is declined before the
// payer is debited, or held briefly and retried against the same bank, as that
// bank has chosen. It is never rerouted: the payee's bank is fixed.
//
// With -propose the switch also measures each bank from its own traffic and
// proposes flag changes; the chaincode accepts them only if the network
// operator's independent measurements agree.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"clearchain/services/internal/fabric"
	"clearchain/services/internal/flagcache"
	"clearchain/services/internal/health"
	"clearchain/services/internal/observer"
)

const (
	outSuccess  = "SUCCESS"
	outDeclined = "DECLINED"        // stopped before any debit
	outPayer    = "PAYER_FAILED"    // payer bank did not debit
	outReversed = "FAILED_REVERSED" // debited, credit refused, debit reversed
	outPending  = "PENDING"         // debited, credit unconfirmed: the stuck state
)

type payRequest struct {
	RRN       string `json:"rrn"`
	PayerBank string `json:"payerBank"`
	PayeeBank string `json:"payeeBank"`
	Amount    int64  `json:"amount"` // paise
}

type payResult struct {
	RRN            string  `json:"rrn"`
	Outcome        string  `json:"outcome"`
	Reason         string  `json:"reason,omitempty"`
	Debited        bool    `json:"debited"`
	PayeeBank      string  `json:"payeeBank"`
	PayeeFlag      string  `json:"payeeFlag"`
	DecisionMicros float64 `json:"decisionMicros"` // time spent on the routing decision
	HeldMs         int64   `json:"heldMs"`
	TotalMs        int64   `json:"totalMs"`
	At             string  `json:"at"`
}

type proposal struct {
	Status         string    `json:"status"`
	Reason         string    `json:"reason"`
	CommittedAt    time.Time `json:"committedAt"`
	CacheUpdatedAt time.Time `json:"cacheUpdatedAt"`
	// CacheLagMs is how long after the commit returned this switch's cache held
	// the new flag. Negative means the event arrived before the commit call returned.
	CacheLagMs int64 `json:"cacheLagMs"`
}

type paySwitch struct {
	name     string
	cache    *flagcache.Cache
	meter    *health.Meter
	cfg      health.Config
	operator string
	http     *http.Client
	contract func(fn string, args ...string) error

	probeSeq sync.Map // bank -> *atomic.Uint64

	mu        sync.Mutex
	outcomes  map[string]int
	reasons   map[string]int
	recent    []payResult
	decisions []float64 // recent decision times, microseconds
	proposals map[string]proposal
}

// admit is the routing decision. It reads only the local cache.
func (s *paySwitch) admit(bank string) (ok bool, reason string, e flagcache.Entry) {
	e, found := s.cache.Get(bank)
	if !found {
		return false, "UNKNOWN_PAYEE_BANK", e
	}
	switch e.Status {
	case flagcache.Healthy:
		return true, "", e
	case flagcache.Probing:
		// Let a small, fixed share of live payments through to test the bank.
		every := uint64(20)
		if e.ProbeShareBps > 0 {
			every = uint64(10000 / e.ProbeShareBps)
		}
		v, _ := s.probeSeq.LoadOrStore(bank, new(atomic.Uint64))
		if v.(*atomic.Uint64).Add(1)%every == 0 {
			return true, "PROBE", e
		}
		return false, "PAYEE_BANK_RECOVERING", e
	default:
		return false, "PAYEE_BANK_DEGRADED", e
	}
}

func (s *paySwitch) pay(ctx context.Context, req payRequest) payResult {
	start := time.Now()
	ok, reason, e := s.admit(req.PayeeBank)
	decision := time.Since(start)
	res := payResult{RRN: req.RRN, PayeeBank: req.PayeeBank, PayeeFlag: e.Status,
		DecisionMicros: float64(decision.Nanoseconds()) / 1000, At: start.Format(time.RFC3339Nano)}

	// The bank asked for its payments to be held and retried rather than failed.
	if !ok && reason != "UNKNOWN_PAYEE_BANK" && e.Mode == flagcache.HoldRetry && e.HoldMs > 0 {
		deadline := time.NewTimer(time.Duration(e.HoldMs) * time.Millisecond)
		defer deadline.Stop()
	hold:
		for !ok {
			changed := s.cache.Changed()
			// Re-check after taking the channel so an update is never missed.
			if cur, found := s.cache.Get(req.PayeeBank); found && cur.Status == flagcache.Healthy {
				ok, reason, e = true, "HELD_THEN_SENT", cur
				break
			}
			select {
			case <-changed:
			case <-deadline.C:
				break hold
			case <-ctx.Done():
				break hold
			}
		}
		res.HeldMs = time.Since(start).Milliseconds()
		res.PayeeFlag = e.Status
		if !ok {
			reason = "PAYEE_BANK_DEGRADED_HOLD_EXPIRED"
		}
	}

	if !ok {
		res.Outcome, res.Reason = outDeclined, reason
		return s.finish(res, start, decision)
	}
	if reason != "" {
		res.Reason = reason
	}

	// Money moves only from here on.
	if good, _ := s.call(ctx, req.PayerBank, "debit", req); !good {
		res.Outcome, res.Reason = outPayer, "PAYER_BANK_DID_NOT_DEBIT"
		return s.finish(res, start, decision)
	}
	res.Debited = true
	good, timedOut := s.call(ctx, req.PayeeBank, "credit", req)
	switch {
	case good:
		res.Outcome = outSuccess
	case timedOut:
		// Debited, but the payee bank never confirmed. This is the ambiguous
		// state health-aware routing exists to avoid.
		res.Outcome, res.Reason = outPending, "PAYEE_BANK_NO_CONFIRMATION"
	default:
		s.call(ctx, req.PayerBank, "reverse", req)
		res.Outcome, res.Reason = outReversed, "PAYEE_BANK_REJECTED"
	}
	return s.finish(res, start, decision)
}

// call sends one request to a bank through the operator hop and measures it.
func (s *paySwitch) call(ctx context.Context, bank, op string, req payRequest) (ok, timedOut bool) {
	body, _ := json.Marshal(map[string]any{"rrn": req.RRN, "amount": req.Amount})
	hreq, _ := http.NewRequestWithContext(ctx, http.MethodPost, s.operator+"/upi/"+bank+"/"+op, bytes.NewReader(body))
	hreq.Header.Set("Content-Type", "application/json")
	start := time.Now()
	resp, err := s.http.Do(hreq)
	elapsed := time.Since(start)
	if err != nil {
		var t interface{ Timeout() bool }
		timedOut = errors.As(err, &t) && t.Timeout()
	} else {
		resp.Body.Close()
		timedOut = resp.StatusCode == http.StatusGatewayTimeout
		ok = resp.StatusCode < 300
	}
	if op != "reverse" {
		s.meter.Record(bank, elapsed, timedOut, !ok)
	}
	return ok, timedOut
}

func (s *paySwitch) finish(res payResult, start time.Time, decision time.Duration) payResult {
	res.TotalMs = time.Since(start).Milliseconds()
	s.mu.Lock()
	s.outcomes[res.Outcome]++
	if res.Reason != "" {
		s.reasons[res.Reason]++
	}
	s.recent = append(s.recent, res)
	if len(s.recent) > 200 {
		s.recent = s.recent[len(s.recent)-200:]
	}
	s.decisions = append(s.decisions, float64(decision.Nanoseconds())/1000)
	if len(s.decisions) > 20000 {
		s.decisions = s.decisions[len(s.decisions)-20000:]
	}
	s.mu.Unlock()
	return res
}

// afterTick runs once per measurement window, after this switch's observations
// are on the ledger, and proposes any flag change its own measurements support.
func (s *paySwitch) afterTick(ctx context.Context, reports []observer.Report) {
	for _, r := range reports {
		var want, reason string
		switch r.Flag.Status {
		case flagcache.Healthy:
			if health.Degraded(r.Stats, s.cfg.MinSample, s.cfg) {
				want = flagcache.Degraded
				reason = fmt.Sprintf("%d of %d timed out, %d failed, p95 %dms", r.Stats.TimeoutCount, r.Stats.TxCount, r.Stats.FailureCount, r.Stats.P95LatencyMs)
			}
		case flagcache.Degraded:
			if time.Since(r.Flag.StatusAt) >= time.Duration(s.cfg.CooldownSeconds+1)*time.Second {
				want, reason = flagcache.Probing, "cool-down over, sending probe payments"
			}
		case flagcache.Probing:
			if r.Stats.TxCount >= s.cfg.ProbeMinSample {
				if health.Degraded(r.Stats, s.cfg.ProbeMinSample, s.cfg) {
					want = flagcache.Degraded
					reason = fmt.Sprintf("probe payments failing: %d of %d timed out", r.Stats.TimeoutCount, r.Stats.TxCount)
				} else {
					want = flagcache.Healthy
					reason = fmt.Sprintf("%d probe payments succeeded", r.Stats.TxCount-r.Stats.TimeoutCount-r.Stats.FailureCount)
				}
			}
		}
		if want == "" {
			continue
		}
		if err := s.contract("ProposeFlag", r.Bank, want, reason); err != nil {
			// Usually the operator's matching observation has not landed yet; the
			// next window tries again.
			log.Printf("%s: proposal %s -> %s not accepted: %v", s.name, r.Bank, want, err)
			continue
		}
		committed := time.Now()
		p := proposal{Status: want, Reason: reason, CommittedAt: committed}
		// The flag reaches the cache through the event stream; wait briefly for it.
		for i := 0; i < 200; i++ {
			if e, ok := s.cache.Get(r.Bank); ok && e.Status == want && e.Seq > r.Flag.Seq {
				p.CacheUpdatedAt = e.UpdatedAt
				p.CacheLagMs = e.UpdatedAt.Sub(committed).Milliseconds()
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		s.mu.Lock()
		s.proposals[r.Bank] = p
		s.mu.Unlock()
		log.Printf("%s: %s flagged %s (%s); cache lag %dms", s.name, r.Bank, want, reason, p.CacheLagMs)
	}
}

func (s *paySwitch) state() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := append([]float64(nil), s.decisions...)
	sort.Float64s(d)
	pct := func(p float64) float64 {
		if len(d) == 0 {
			return 0
		}
		return d[int(float64(len(d)-1)*p)]
	}
	outcomes := map[string]int{}
	for k, v := range s.outcomes {
		outcomes[k] = v
	}
	reasons := map[string]int{}
	for k, v := range s.reasons {
		reasons[k] = v
	}
	proposals := map[string]proposal{}
	for k, v := range s.proposals {
		proposals[k] = v
	}
	recent := append([]payResult(nil), s.recent...)
	return map[string]any{
		"switch":   s.name,
		"flags":    s.cache.All(),
		"outcomes": outcomes,
		"reasons":  reasons,
		"decisionMicros": map[string]any{
			"samples": len(d), "p50": pct(0.50), "p99": pct(0.99), "max": pct(1),
		},
		"proposals": proposals,
		"recent":    recent,
	}
}

func main() {
	name := flag.String("name", "switch-1", "name shown in logs and state")
	addr := flag.String("addr", ":9100", "listen address")
	operator := flag.String("operator", "http://localhost:9102", "network operator hop base URL")
	channel := flag.String("channel", "mychannel", "channel name")
	window := flag.Duration("window", 5*time.Second, "measurement window")
	propose := flag.Bool("propose", true, "measure banks and propose flag changes")
	timeout := flag.Duration("bank-timeout", 3500*time.Millisecond, "how long to wait for a bank via the operator")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	cache := flagcache.New()
	if err := cache.Start(ctx, "switch", *channel); err != nil {
		log.Fatalf("%s: %v", *name, err)
	}
	cfg, err := flagcache.LoadConfig("switch", *channel)
	if err != nil {
		log.Fatalf("%s: %v", *name, err)
	}

	s := &paySwitch{
		name: *name, cache: cache, meter: health.NewMeter(), cfg: cfg, operator: *operator,
		http:     &http.Client{Timeout: *timeout, Transport: &http.Transport{MaxIdleConnsPerHost: 256}},
		outcomes: map[string]int{}, reasons: map[string]int{}, proposals: map[string]proposal{},
	}

	if *propose {
		cl, err := fabric.Connect("switch", "User1", *channel)
		if err != nil {
			log.Fatalf("%s: %v", *name, err)
		}
		defer cl.Close()
		contract := cl.Contract(flagcache.Chaincode)
		s.contract = func(fn string, args ...string) error {
			_, err := contract.SubmitTransaction(fn, args...)
			return err
		}
		obs := &observer.Observer{Org: "switch", Channel: *channel, Window: *window, Meter: s.meter, Cache: cache, AfterTick: s.afterTick}
		go func() {
			if err := obs.Run(ctx); err != nil {
				log.Fatalf("%s: observer: %v", *name, err)
			}
		}()
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /pay", func(w http.ResponseWriter, r *http.Request) {
		var req payRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RRN == "" || req.PayerBank == "" || req.PayeeBank == "" || req.Amount <= 0 {
			http.Error(w, "rrn, payerBank, payeeBank and a positive amount are required", http.StatusBadRequest)
			return
		}
		// A payment in flight is not abandoned because the caller hung up.
		res := s.pay(context.WithoutCancel(r.Context()), req)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(res)
	})
	mux.HandleFunc("GET /state", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		json.NewEncoder(w).Encode(s.state())
	})
	srv := &http.Server{Addr: *addr, Handler: mux}
	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	log.Printf("%s listening on %s (propose=%v, window=%v, thresholds=%+v)", *name, *addr, *propose, *window, cfg)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
