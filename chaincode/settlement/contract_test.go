package main

import (
	"fmt"
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
}

func (s *fakeStub) GetState(key string) ([]byte, error) { return s.state[key], nil }
func (s *fakeStub) PutState(key string, v []byte) error { s.state[key] = v; return nil }
func (s *fakeStub) DelState(key string) error           { delete(s.state, key); return nil }
func (s *fakeStub) GetTxID() string                     { return s.txID }
func (s *fakeStub) GetTxTimestamp() (*timestamppb.Timestamp, error) {
	return &timestamppb.Timestamp{Seconds: s.now}, nil
}
func (s *fakeStub) SetEvent(name string, _ []byte) error { s.eventName = name; return nil }
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

func (c *fakeCtx) GetStub() shim.ChaincodeStubInterface  { return c.stub }
func (c *fakeCtx) GetClientIdentity() cid.ClientIdentity { return c.id }

type world struct {
	t    *testing.T
	stub *fakeStub
	cc   *SettlementContract
	n    int
}

func (w *world) as(msp string) *fakeCtx {
	w.n++
	w.stub.txID = fmt.Sprintf("tx%04d", w.n)
	w.stub.eventName = ""
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

const (
	bankAMSP = "BankAOrgMSP"
	hashA    = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	hashB    = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// setup registers two banks, one merchant (2% fee, 18% GST on the fee) and a float.
func setup(t *testing.T) *world {
	w := &world{t: t, stub: &fakeStub{state: map[string][]byte{}, now: 2_000_000}, cc: &SettlementContract{}}
	w.must(w.cc.RegisterBank(w.as(OperatorMSP), "BANKA", bankAMSP))
	w.must(w.cc.RegisterBank(w.as(OperatorMSP), "SPONSORBANK", SponsorMSP))
	w.must(w.cc.RegisterMerchant(w.as(AggregatorMSP), "M001", "Raju Electronics", 200, 1800, "SPONSORBANK", hashA))
	_, err := w.cc.FundFloat(w.as(SponsorMSP), 32_000_000, "FUND-1")
	w.must(err)
	return w
}

func (w *world) record(rrn string, amount int64) {
	w.t.Helper()
	_, err := w.cc.RecordPayment(w.as(SwitchMSP), rrn, "M001", "BANKA", "SPONSORBANK", amount, w.stub.now, hashA, hashB)
	w.must(err)
}

func (w *world) float() int64 {
	w.t.Helper()
	f, err := w.cc.GetFloat(w.as(AggregatorMSP))
	w.must(err)
	return f.Total
}

// ---- tests ------------------------------------------------------------------

func TestSplitAddsUp(t *testing.T) {
	for _, gross := range []int64{1, 99, 100, 10_000, 123_457, 99_999_999} {
		fee, gst, net := split(gross, 200, 1800)
		if fee+gst+net != gross || fee < 0 || gst < 0 || net < 0 {
			t.Fatalf("split(%d) = %d + %d + %d does not add up", gross, fee, gst, net)
		}
	}
	// Rs 1,000.00 at 2% fee and 18% GST on the fee: fee Rs 20, GST Rs 3.60, merchant Rs 976.40.
	if fee, gst, net := split(100_000, 200, 1800); fee != 2_000 || gst != 360 || net != 97_640 {
		t.Fatalf("unexpected split: %d %d %d", fee, gst, net)
	}
}

func TestDuplicateRRNRejected(t *testing.T) {
	w := setup(t)
	w.record("RRN000001", 100_000)
	if w.stub.eventName != EventRecorded {
		t.Fatalf("recording should emit %s", EventRecorded)
	}
	_, err := w.cc.RecordPayment(w.as(SwitchMSP), "RRN000001", "M001", "BANKA", "SPONSORBANK", 100_000, w.stub.now, hashA, hashB)
	w.wantErr(err, "duplicate: payment RRN000001 is already recorded")
	// Same RRN with different details is still a duplicate.
	_, err = w.cc.RecordPayment(w.as(SwitchMSP), "RRN000001", "", "BANKA", "SPONSORBANK", 5, w.stub.now, hashA, hashB)
	w.wantErr(err, "duplicate")
}

func TestRecordPaymentValidation(t *testing.T) {
	w := setup(t)
	rec := func(msp, rrn, merchant, payee string, amount, at int64, h string) error {
		_, err := w.cc.RecordPayment(w.as(msp), rrn, merchant, "BANKA", payee, amount, at, h, hashB)
		return err
	}
	w.wantErr(rec(AggregatorMSP, "RRN000002", "M001", "SPONSORBANK", 100, w.stub.now, hashA), "not allowed to record payments")
	w.wantErr(rec(SwitchMSP, "bad rrn", "M001", "SPONSORBANK", 100, w.stub.now, hashA), "rrn must be")
	w.wantErr(rec(SwitchMSP, "RRN000002", "M001", "SPONSORBANK", 0, w.stub.now, hashA), "amount must be positive")
	w.wantErr(rec(SwitchMSP, "RRN000002", "M001", "SPONSORBANK", 100, w.stub.now, "9876543210"), "never stored")
	w.wantErr(rec(SwitchMSP, "RRN000002", "M001", "NOBANK", 100, w.stub.now, hashA), "not registered")
	w.wantErr(rec(SwitchMSP, "RRN000002", "M404", "SPONSORBANK", 100, w.stub.now, hashA), "merchant M404 is not registered")
	w.wantErr(rec(SwitchMSP, "RRN000002", "M001", "BANKA", 100, w.stub.now, hashA), "settled through escrow at SPONSORBANK")
	w.wantErr(rec(SwitchMSP, "RRN000002", "M001", "SPONSORBANK", 100, w.stub.now+60, hashA), "in the future")
}

func TestDeadlineRunsFromEarliestTimestamp(t *testing.T) {
	w := setup(t)
	// The switch debited 40 seconds before the record reached the ledger.
	debitedAt := w.stub.now - 40
	p, err := w.cc.RecordPayment(w.as(SwitchMSP), "RRN000003", "M001", "BANKA", "SPONSORBANK", 100, debitedAt, hashA, hashB)
	w.must(err)
	if p.EarliestAt != debitedAt || p.DeadlineAt != debitedAt+defaultConfig().TatSeconds {
		t.Fatalf("deadline must run from the debit, not the delayed record: %+v", p)
	}
}

func TestPayoutNeedsConfirmedEscrowCredit(t *testing.T) {
	w := setup(t)
	before := w.float()
	w.record("RRN000004", 100_000)

	_, err := w.cc.PayoutMerchant(w.as(AggregatorMSP), "RRN000004")
	w.wantErr(err, "needs the escrow credit to be confirmed first")
	if w.float() != before {
		t.Fatalf("a refused payout must not touch the float")
	}

	// Only the payee bank's org can confirm.
	for _, msp := range []string{AggregatorMSP, SwitchMSP, bankAMSP} {
		_, err = w.cc.ConfirmCredit(w.as(msp), "RRN000004")
		w.wantErr(err, "only the payee bank's org")
	}
	w.tick(2)
	_, err = w.cc.ConfirmCredit(w.as(SponsorMSP), "RRN000004")
	w.must(err)
	_, err = w.cc.ConfirmCredit(w.as(SponsorMSP), "RRN000004")
	w.wantErr(err, "duplicate")

	// Only the aggregator can request the payout.
	_, err = w.cc.PayoutMerchant(w.as(SponsorMSP), "RRN000004")
	w.wantErr(err, "not allowed to request merchant payouts")
	w.tick(1)
	out, err := w.cc.PayoutMerchant(w.as(AggregatorMSP), "RRN000004")
	w.must(err)
	if out.Fee != 2_000 || out.Gst != 360 || out.MerchantAmount != 97_640 || out.Gross != 100_000 {
		t.Fatalf("unexpected payout split: %+v", out)
	}
	if out.PaidOutAt-out.ConfirmedAt != 1 {
		t.Fatalf("payout should record both timestamps: %+v", out)
	}
	if got := w.float(); got != before-97_640 {
		t.Fatalf("float should fall by the merchant amount: before %d after %d", before, got)
	}

	_, err = w.cc.PayoutMerchant(w.as(AggregatorMSP), "RRN000004")
	w.wantErr(err, "duplicate: payment RRN000004 was already paid out")
	if got := w.float(); got != before-97_640 {
		t.Fatalf("a duplicate payout must not touch the float")
	}
}

func TestNonMerchantPaymentHasNoPayout(t *testing.T) {
	w := setup(t)
	_, err := w.cc.RecordPayment(w.as(SwitchMSP), "RRN000005", "", "BANKA", "SPONSORBANK", 100, w.stub.now, hashA, hashB)
	w.must(err)
	_, err = w.cc.ConfirmCredit(w.as(SponsorMSP), "RRN000005")
	w.must(err)
	_, err = w.cc.PayoutMerchant(w.as(AggregatorMSP), "RRN000005")
	w.wantErr(err, "not a merchant payment")
}

func TestFloatExhausted(t *testing.T) {
	w := &world{t: t, stub: &fakeStub{state: map[string][]byte{}, now: 2_000_000}, cc: &SettlementContract{}}
	w.must(w.cc.RegisterBank(w.as(OperatorMSP), "BANKA", bankAMSP))
	w.must(w.cc.RegisterBank(w.as(OperatorMSP), "SPONSORBANK", SponsorMSP))
	w.must(w.cc.RegisterMerchant(w.as(AggregatorMSP), "M001", "Raju Electronics", 200, 1800, "SPONSORBANK", hashA))
	_, err := w.cc.FundFloat(w.as(AggregatorMSP), 3200, "FUND-1")
	w.wantErr(err, "not allowed to fund the float")
	_, err = w.cc.FundFloat(w.as(SponsorMSP), 3200, "FUND-1") // 100 paise per lane
	w.must(err)
	_, err = w.cc.FundFloat(w.as(SponsorMSP), 3200, "FUND-1")
	w.wantErr(err, "duplicate: funding")

	w.record("RRN000006", 100_000)
	_, err = w.cc.ConfirmCredit(w.as(SponsorMSP), "RRN000006")
	w.must(err)
	_, err = w.cc.PayoutMerchant(w.as(AggregatorMSP), "RRN000006")
	w.wantErr(err, "float exhausted")
}

func TestPayoutFallsThroughToAnotherLane(t *testing.T) {
	w := setup(t)
	// Drain the payment's own lane; the payout must come from the next one.
	own := laneFor("RRN000007")
	w.must(putJSON(w.as(SponsorMSP), laneKey(own), Lane{DocType: "lane", Lane: own, Balance: 10}))
	w.record("RRN000007", 100_000)
	_, err := w.cc.ConfirmCredit(w.as(SponsorMSP), "RRN000007")
	w.must(err)
	out, err := w.cc.PayoutMerchant(w.as(AggregatorMSP), "RRN000007")
	w.must(err)
	if out.Lane != (own+1)%FloatLanes {
		t.Fatalf("expected the next lane %d, got %d", (own+1)%FloatLanes, out.Lane)
	}
}

func TestReimburseFloat(t *testing.T) {
	w := setup(t)
	before := w.float()
	rrns := []string{"RRN000010", "RRN000011", "RRN000012"}
	for _, r := range rrns {
		w.record(r, 100_000)
		_, err := w.cc.ConfirmCredit(w.as(SponsorMSP), r)
		w.must(err)
		_, err = w.cc.PayoutMerchant(w.as(AggregatorMSP), r)
		w.must(err)
	}
	if got := w.float(); got != before-3*97_640 {
		t.Fatalf("float after three payouts: %d", got)
	}
	list := `["RRN000010","RRN000011","RRN000012"]`
	_, err := w.cc.ReimburseFloat(w.as(AggregatorMSP), "CYCLE-1", list)
	w.wantErr(err, "not allowed to reimburse the float")
	_, err = w.cc.ReimburseFloat(w.as(SponsorMSP), "CYCLE-1", `["RRN000010","RRN000010"]`)
	w.wantErr(err, "listed twice")
	_, err = w.cc.ReimburseFloat(w.as(SponsorMSP), "CYCLE-1", `["RRN999999"]`)
	w.wantErr(err, "no payout to reimburse")

	cy, err := w.cc.ReimburseFloat(w.as(SponsorMSP), "CYCLE-1", list)
	w.must(err)
	if cy.Count != 3 || cy.TotalReturned != 3*97_640 || cy.TotalFee != 6_000 || cy.TotalGst != 1_080 || cy.TotalGross != 300_000 {
		t.Fatalf("unexpected cycle totals: %+v", cy)
	}
	if got := w.float(); got != before {
		t.Fatalf("float should be whole again: before %d after %d", before, got)
	}
	_, err = w.cc.ReimburseFloat(w.as(SponsorMSP), "CYCLE-1", list)
	w.wantErr(err, "duplicate: settlement cycle")
	_, err = w.cc.ReimburseFloat(w.as(SponsorMSP), "CYCLE-2", list)
	w.wantErr(err, "already reimbursed")
	p, err := w.cc.GetPayment(w.as(SwitchMSP), "RRN000010")
	w.must(err)
	if p.Status != StatusReimbursed {
		t.Fatalf("expected REIMBURSED, got %s", p.Status)
	}
}

func TestAutoReversal(t *testing.T) {
	w := setup(t)
	w.must(w.cc.SetConfig(w.as(OperatorMSP), `{"tatSeconds":60,"reverseMarginSeconds":15}`))
	before := w.float()
	w.record("RRN000020", 100_000)

	pend, err := w.cc.GetPendingPayments(w.as(OperatorMSP))
	w.must(err)
	if len(pend) != 1 || pend[0].RRN != "RRN000020" || pend[0].DeadlineAt != w.stub.now+60 {
		t.Fatalf("unexpected pending list: %+v", pend)
	}

	// Parties with no role here cannot trigger it.
	for _, msp := range []string{AggregatorMSP, bankAMSP, SponsorMSP} {
		_, err = w.cc.RequestReversal(w.as(msp), "RRN000020")
		w.wantErr(err, "not allowed to request a reversal")
	}
	// The watcher asks too soon: the contract checks the on-chain timestamp itself.
	w.tick(30)
	_, err = w.cc.RequestReversal(w.as(OperatorMSP), "RRN000020")
	w.wantErr(err, "too early: the reversal window for RRN000020 opens in 15s")

	w.tick(15) // 45s after the debit: the window opens 15s before the 60s deadline
	rev, err := w.cc.RequestReversal(w.as(OperatorMSP), "RRN000020")
	w.must(err)
	if !rev.WithinDeadline || rev.SecondsBeforeDeadline != 15 || rev.CompensationPaise != 0 || rev.FloatAdvanced {
		t.Fatalf("unexpected reversal: %+v", rev)
	}
	if w.stub.eventName != EventReversed {
		t.Fatalf("reversal should emit %s", EventReversed)
	}
	if w.float() != before {
		t.Fatalf("a reversal must not touch the float")
	}
	pend, err = w.cc.GetPendingPayments(w.as(OperatorMSP))
	w.must(err)
	if len(pend) != 0 {
		t.Fatalf("reversed payment should leave the pending list")
	}

	// Once reversed it cannot be reversed again, confirmed, or paid out.
	_, err = w.cc.RequestReversal(w.as(OperatorMSP), "RRN000020")
	w.wantErr(err, "only a debited, unconfirmed payment can be reversed")
	_, err = w.cc.ConfirmCredit(w.as(SponsorMSP), "RRN000020")
	w.wantErr(err, "was auto-reversed")
	_, err = w.cc.PayoutMerchant(w.as(AggregatorMSP), "RRN000020")
	w.wantErr(err, "needs the escrow credit to be confirmed first")
}

func TestConfirmedPaymentCannotBeReversed(t *testing.T) {
	w := setup(t)
	w.must(w.cc.SetConfig(w.as(OperatorMSP), `{"tatSeconds":60,"reverseMarginSeconds":15}`))
	w.record("RRN000021", 100_000)
	_, err := w.cc.ConfirmCredit(w.as(SponsorMSP), "RRN000021")
	w.must(err)
	w.tick(600)
	_, err = w.cc.RequestReversal(w.as(OperatorMSP), "RRN000021")
	w.wantErr(err, "only a debited, unconfirmed payment can be reversed")
}

func TestLateReversalRecordsCompensation(t *testing.T) {
	w := setup(t)
	w.record("RRN000022", 100_000) // default rule: T+1 day
	w.tick(86400 + 86400 + 1)      // deadline missed by a day and a second
	rev, err := w.cc.RequestReversal(w.as(SwitchMSP), "RRN000022")
	w.must(err)
	if rev.WithinDeadline || rev.CompensationPaise != 2*defaultConfig().CompensationPaisePerDay {
		t.Fatalf("two days late should owe two days of compensation: %+v", rev)
	}
}

func TestConfigRules(t *testing.T) {
	w := setup(t)
	w.wantErr(w.cc.SetConfig(w.as(SwitchMSP), `{"tatSeconds":60}`), "not allowed to change")
	w.wantErr(w.cc.SetConfig(w.as(OperatorMSP), `{"tatSeconds":60,"reverseMarginSeconds":60}`), "tatSeconds must be positive")
	w.wantErr(w.cc.SetConfig(w.as(OperatorMSP), `{"surprise":1}`), "invalid config")
	// A rule change does not move the deadline of a payment already recorded.
	w.record("RRN000030", 100)
	w.must(w.cc.SetConfig(w.as(OperatorMSP), `{"tatSeconds":60,"reverseMarginSeconds":15}`))
	p, err := w.cc.GetPayment(w.as(SwitchMSP), "RRN000030")
	w.must(err)
	if p.DeadlineAt != p.EarliestAt+86400 {
		t.Fatalf("existing deadline moved: %+v", p)
	}
}

func TestMerchantRules(t *testing.T) {
	w := setup(t)
	reg := func(msp, id string, fee int64, bank, h string) error {
		return w.cc.RegisterMerchant(w.as(msp), id, "Shop", fee, 1800, bank, h)
	}
	w.wantErr(reg(SponsorMSP, "M002", 200, "SPONSORBANK", hashA), "not allowed to register merchants")
	w.wantErr(reg(AggregatorMSP, "M002", 900, "SPONSORBANK", hashA), "feeBps must be")
	w.wantErr(reg(AggregatorMSP, "M002", 200, "BANKA", hashA), "not operated by the sponsor bank")
	w.wantErr(reg(AggregatorMSP, "M002", 200, "SPONSORBANK", "ACC-12345"), "never stored")
	w.wantErr(reg(AggregatorMSP, "M001", 200, "SPONSORBANK", hashA), "already registered")
}
