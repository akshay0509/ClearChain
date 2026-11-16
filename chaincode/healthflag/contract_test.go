package main

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/hyperledger/fabric-chaincode-go/v2/pkg/cid"
	"github.com/hyperledger/fabric-chaincode-go/v2/shim"
	"github.com/hyperledger/fabric-contract-api-go/v2/contractapi"
	"github.com/hyperledger/fabric-protos-go-apiv2/ledger/queryresult"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ---- in-memory ledger -------------------------------------------------------

type fakeStub struct {
	shim.ChaincodeStubInterface
	state     map[string][]byte
	now       int64
	txID      string
	eventName string
	event     []byte
}

func (s *fakeStub) GetState(key string) ([]byte, error) { return s.state[key], nil }
func (s *fakeStub) PutState(key string, v []byte) error { s.state[key] = v; return nil }
func (s *fakeStub) GetTxID() string                     { return s.txID }
func (s *fakeStub) GetTxTimestamp() (*timestamppb.Timestamp, error) {
	return &timestamppb.Timestamp{Seconds: s.now}, nil
}
func (s *fakeStub) SetEvent(name string, payload []byte) error {
	s.eventName, s.event = name, payload
	return nil
}
func (s *fakeStub) GetStateByRange(start, end string) (shim.StateQueryIteratorInterface, error) {
	var keys []string
	for k := range s.state {
		if k >= start && k < end {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	it := &fakeIter{}
	for _, k := range keys {
		it.kvs = append(it.kvs, &queryresult.KV{Key: k, Value: s.state[k]})
	}
	return it, nil
}

type fakeIter struct {
	kvs []*queryresult.KV
	pos int
}

func (i *fakeIter) HasNext() bool { return i.pos < len(i.kvs) }
func (i *fakeIter) Close() error  { return nil }
func (i *fakeIter) Next() (*queryresult.KV, error) {
	kv := i.kvs[i.pos]
	i.pos++
	return kv, nil
}

type fakeID struct {
	cid.ClientIdentity
	msp string
}

func (f *fakeID) GetMSPID() (string, error) { return f.msp, nil }

type fakeCtx struct {
	contractapi.TransactionContextInterface
	stub *fakeStub
	id   *fakeID
}

func (c *fakeCtx) GetStub() shim.ChaincodeStubInterface   { return c.stub }
func (c *fakeCtx) GetClientIdentity() cid.ClientIdentity { return c.id }

// world is one ledger that different orgs transact against at a moving clock.
type world struct {
	t    *testing.T
	stub *fakeStub
	cc   *HealthContract
	n    int
}

func newWorld(t *testing.T) *world {
	return &world{t: t, stub: &fakeStub{state: map[string][]byte{}, now: 1_000_000}, cc: &HealthContract{}}
}

func (w *world) as(msp string) *fakeCtx {
	w.n++
	w.stub.txID = "tx" + string(rune('a'+w.n%26)) + strings.Repeat("x", w.n/26)
	w.stub.eventName, w.stub.event = "", nil
	return &fakeCtx{stub: w.stub, id: &fakeID{msp: msp}}
}

func (w *world) tick(seconds int64) { w.stub.now += seconds }

func (w *world) must(err error) {
	w.t.Helper()
	if err != nil {
		w.t.Fatalf("unexpected error: %v", err)
	}
}

func (w *world) wantErr(err error, contains string) {
	w.t.Helper()
	if err == nil {
		w.t.Fatalf("expected an error containing %q, got none", contains)
	}
	if !strings.Contains(err.Error(), contains) {
		w.t.Fatalf("expected an error containing %q, got: %v", contains, err)
	}
}

func (w *world) status(bank string) string {
	w.t.Helper()
	f, err := w.cc.GetBankFlag(w.as(SwitchMSP), bank)
	w.must(err)
	return f.Status
}

// observe records the same measurement from both the switch and the operator.
func (w *world) observe(bank string, tx, timeouts, failures, p95 int) {
	w.t.Helper()
	w.must(w.cc.SubmitObservation(w.as(SwitchMSP), bank, 10, tx, timeouts, failures, p95))
	w.must(w.cc.SubmitObservation(w.as(OperatorMSP), bank, 10, tx, timeouts, failures, p95))
}

func setup(t *testing.T) *world {
	w := newWorld(t)
	w.must(w.cc.RegisterBank(w.as(OperatorMSP), "BANKB", "Bank B", "BankBOrgMSP"))
	return w
}

// ---- tests ------------------------------------------------------------------

func TestClassify(t *testing.T) {
	cfg := defaultConfig()
	cases := []struct {
		name string
		obs  Observation
		want string
	}{
		{"clean window", Observation{TxCount: 100, P95LatencyMs: 200}, StatusHealthy},
		{"too few payments to judge", Observation{TxCount: 5, TimeoutCount: 5}, StatusHealthy},
		{"no payments", Observation{}, StatusHealthy},
		{"timeouts at threshold", Observation{TxCount: 100, TimeoutCount: 5}, StatusDegraded},
		{"timeouts just under threshold", Observation{TxCount: 1000, TimeoutCount: 49}, StatusHealthy},
		{"failures at threshold", Observation{TxCount: 100, FailureCount: 10}, StatusDegraded},
		{"slow responses", Observation{TxCount: 100, P95LatencyMs: 3000}, StatusDegraded},
	}
	for _, c := range cases {
		if got := classify(c.obs, cfg.MinSample, cfg); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

func TestRegisterBank(t *testing.T) {
	w := newWorld(t)
	w.wantErr(w.cc.RegisterBank(w.as(SwitchMSP), "BANKB", "Bank B", "BankBOrgMSP"), "only the network operator")
	w.wantErr(w.cc.RegisterBank(w.as(OperatorMSP), "bank b", "Bank B", "BankBOrgMSP"), "bankId must be")
	w.must(w.cc.RegisterBank(w.as(OperatorMSP), "BANKB", "Bank B", "BankBOrgMSP"))
	if w.stub.eventName != EventFlagChanged {
		t.Fatalf("registration should emit %s, got %q", EventFlagChanged, w.stub.eventName)
	}
	w.wantErr(w.cc.RegisterBank(w.as(OperatorMSP), "BANKB", "Bank B", "BankBOrgMSP"), "already registered")
	if got := w.status("BANKB"); got != StatusHealthy {
		t.Fatalf("new bank should be HEALTHY, got %s", got)
	}
}

func TestObservationAccess(t *testing.T) {
	w := setup(t)
	// The bank being measured cannot report on itself.
	w.wantErr(w.cc.SubmitObservation(w.as("BankBOrgMSP"), "BANKB", 10, 100, 0, 0, 100), "only a switch")
	w.wantErr(w.cc.SubmitObservation(w.as(SwitchMSP), "NOBANK", 10, 100, 0, 0, 100), "not registered")
	w.wantErr(w.cc.SubmitObservation(w.as(SwitchMSP), "BANKB", 10, 10, 8, 8, 100), "exceed payments observed")
	w.must(w.cc.SubmitObservation(w.as(SwitchMSP), "BANKB", 10, 100, 1, 0, 100))
}

func TestDegradeNeedsBothObservers(t *testing.T) {
	w := setup(t)

	// Only the switch sees trouble: no operator observation at all.
	w.must(w.cc.SubmitObservation(w.as(SwitchMSP), "BANKB", 10, 100, 30, 0, 4000))
	_, err := w.cc.ProposeFlag(w.as(SwitchMSP), "BANKB", StatusDegraded, "timeouts")
	w.wantErr(err, "no network operator observation")

	// The operator has measured, and sees a healthy bank: the switch is overruled.
	w.must(w.cc.SubmitObservation(w.as(OperatorMSP), "BANKB", 10, 100, 0, 0, 150))
	_, err = w.cc.ProposeFlag(w.as(SwitchMSP), "BANKB", StatusDegraded, "timeouts")
	w.wantErr(err, "network operator's observation does not show")
	if got := w.status("BANKB"); got != StatusHealthy {
		t.Fatalf("flag must be unchanged, got %s", got)
	}

	// The operator agrees: the flag changes and an event is emitted.
	w.must(w.cc.SubmitObservation(w.as(OperatorMSP), "BANKB", 10, 100, 25, 0, 3800))
	f, err := w.cc.ProposeFlag(w.as(SwitchMSP), "BANKB", StatusDegraded, "timeouts")
	w.must(err)
	if f.Status != StatusDegraded || f.Seq != 1 || f.OperatorObs.TimeoutCount != 25 {
		t.Fatalf("unexpected flag: %+v", f)
	}
	var ev BankFlag
	if w.stub.eventName != EventFlagChanged || json.Unmarshal(w.stub.event, &ev) != nil || ev.Status != StatusDegraded {
		t.Fatalf("expected a %s event carrying DEGRADED, got %q %s", EventFlagChanged, w.stub.eventName, w.stub.event)
	}
}

func TestOnlySwitchMayPropose(t *testing.T) {
	w := setup(t)
	w.observe("BANKB", 100, 30, 0, 4000)
	for _, msp := range []string{"BankBOrgMSP", OperatorMSP, "AggregatorOrgMSP"} {
		_, err := w.cc.ProposeFlag(w.as(msp), "BANKB", StatusDegraded, "x")
		w.wantErr(err, "only a switch")
	}
}

func TestStaleEvidenceRejected(t *testing.T) {
	w := setup(t)
	w.observe("BANKB", 100, 30, 0, 4000)
	w.tick(int64(defaultConfig().FreshnessSeconds) + 1)
	_, err := w.cc.ProposeFlag(w.as(SwitchMSP), "BANKB", StatusDegraded, "timeouts")
	w.wantErr(err, "stale")
}

func TestSwitchMustHaveItsOwnEvidence(t *testing.T) {
	w := setup(t)
	w.must(w.cc.SubmitObservation(w.as(SwitchMSP), "BANKB", 10, 100, 0, 0, 100))
	w.must(w.cc.SubmitObservation(w.as(OperatorMSP), "BANKB", 10, 100, 30, 0, 4000))
	_, err := w.cc.ProposeFlag(w.as(SwitchMSP), "BANKB", StatusDegraded, "x")
	w.wantErr(err, "switch's own observation does not show")
}

func TestRecoveryCycle(t *testing.T) {
	w := setup(t)
	cfg := defaultConfig()
	w.observe("BANKB", 100, 30, 0, 4000)
	_, err := w.cc.ProposeFlag(w.as(SwitchMSP), "BANKB", StatusDegraded, "timeouts")
	w.must(err)

	// Cannot jump straight back to healthy, nor probe before the cool-down.
	_, err = w.cc.ProposeFlag(w.as(SwitchMSP), "BANKB", StatusHealthy, "x")
	w.wantErr(err, "must be PROBING")
	_, err = w.cc.ProposeFlag(w.as(SwitchMSP), "BANKB", StatusProbing, "x")
	w.wantErr(err, "cool-down not over")

	w.tick(int64(cfg.CooldownSeconds))
	f, err := w.cc.ProposeFlag(w.as(SwitchMSP), "BANKB", StatusProbing, "cool-down over")
	w.must(err)
	if f.ProbeShareBps != cfg.ProbeShareBps {
		t.Fatalf("probing flag should carry the probe share, got %d", f.ProbeShareBps)
	}

	// Evidence from before probing began does not count.
	_, err = w.cc.ProposeFlag(w.as(SwitchMSP), "BANKB", StatusHealthy, "x")
	w.wantErr(err, "predates probing")

	// Too few probe payments so far.
	w.tick(5)
	w.observe("BANKB", cfg.ProbeMinSample-1, 0, 0, 200)
	_, err = w.cc.ProposeFlag(w.as(SwitchMSP), "BANKB", StatusHealthy, "x")
	w.wantErr(err, "probe payments")

	// Probe payments succeed for both observers: back to healthy.
	w.tick(5)
	w.observe("BANKB", cfg.ProbeMinSample, 0, 0, 200)
	_, err = w.cc.ProposeFlag(w.as(SwitchMSP), "BANKB", StatusHealthy, "probes succeeded")
	w.must(err)
	if got := w.status("BANKB"); got != StatusHealthy {
		t.Fatalf("expected HEALTHY, got %s", got)
	}

	hist, err := w.cc.GetFlagHistory(w.as(SwitchMSP), "BANKB")
	w.must(err)
	var seen []string
	for _, h := range hist {
		seen = append(seen, h.Status)
	}
	if got := strings.Join(seen, ","); got != "HEALTHY,DEGRADED,PROBING,HEALTHY" {
		t.Fatalf("unexpected history: %s", got)
	}
}

func TestFailedProbeGoesBackToDegraded(t *testing.T) {
	w := setup(t)
	cfg := defaultConfig()
	w.observe("BANKB", 100, 30, 0, 4000)
	_, err := w.cc.ProposeFlag(w.as(SwitchMSP), "BANKB", StatusDegraded, "timeouts")
	w.must(err)
	w.tick(int64(cfg.CooldownSeconds))
	_, err = w.cc.ProposeFlag(w.as(SwitchMSP), "BANKB", StatusProbing, "cool-down over")
	w.must(err)

	w.tick(5)
	w.observe("BANKB", cfg.ProbeMinSample, cfg.ProbeMinSample/2, 0, 4000)
	_, err = w.cc.ProposeFlag(w.as(SwitchMSP), "BANKB", StatusHealthy, "x")
	w.wantErr(err, "still failing")
	_, err = w.cc.ProposeFlag(w.as(SwitchMSP), "BANKB", StatusDegraded, "probes failed")
	w.must(err)
	if got := w.status("BANKB"); got != StatusDegraded {
		t.Fatalf("expected DEGRADED, got %s", got)
	}
}

func TestBankChoosesItsDegradedBehaviour(t *testing.T) {
	w := setup(t)
	// Another org, including the switch and the operator, cannot choose for the bank.
	w.wantErr(w.cc.SetBankMode(w.as(SwitchMSP), "BANKB", ModeHoldRetry, 2000), "only BankBOrgMSP")
	w.wantErr(w.cc.SetBankMode(w.as("BankBOrgMSP"), "BANKB", ModeHoldRetry, 0), "holdMs must be")
	w.wantErr(w.cc.SetBankMode(w.as("BankBOrgMSP"), "BANKB", "REROUTE", 0), "mode must be")
	w.must(w.cc.SetBankMode(w.as("BankBOrgMSP"), "BANKB", ModeHoldRetry, 2000))

	f, err := w.cc.GetBankFlag(w.as(SwitchMSP), "BANKB")
	w.must(err)
	// The bank changed how switches treat it, not its status.
	if f.Mode != ModeHoldRetry || f.HoldMs != 2000 || f.Status != StatusHealthy {
		t.Fatalf("unexpected flag after mode change: %+v", f)
	}
	if w.stub.eventName != "" {
		t.Fatalf("read should not emit an event")
	}
}

func TestConfig(t *testing.T) {
	w := setup(t)
	w.wantErr(w.cc.SetConfig(w.as(SwitchMSP), `{"cooldownSeconds":5}`), "only the network operator")
	w.wantErr(w.cc.SetConfig(w.as(OperatorMSP), `{"timeoutRateBps":0}`), "basis points")
	w.wantErr(w.cc.SetConfig(w.as(OperatorMSP), `{"nonsense":1}`), "invalid config")
	w.must(w.cc.SetConfig(w.as(OperatorMSP), `{"cooldownSeconds":5,"minSample":10}`))
	cfg, err := w.cc.GetConfig(w.as(SwitchMSP))
	w.must(err)
	if cfg.CooldownSeconds != 5 || cfg.MinSample != 10 || cfg.TimeoutRateBps != defaultConfig().TimeoutRateBps {
		t.Fatalf("partial update should keep other defaults: %+v", cfg)
	}
}

func TestGetAllFlags(t *testing.T) {
	w := setup(t)
	w.must(w.cc.RegisterBank(w.as(OperatorMSP), "BANKA", "Bank A", "BankAOrgMSP"))
	flags, err := w.cc.GetAllFlags(w.as(SwitchMSP))
	w.must(err)
	if len(flags) != 2 || flags[0].BankID != "BANKA" || flags[1].BankID != "BANKB" {
		t.Fatalf("expected BANKA and BANKB, got %d flags", len(flags))
	}
}
