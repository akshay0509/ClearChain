package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/hyperledger/fabric-chaincode-go/v2/pkg/cid"
	"github.com/hyperledger/fabric-chaincode-go/v2/shim"
	"github.com/hyperledger/fabric-contract-api-go/v2/contractapi"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ---- in-memory ledger -------------------------------------------------------

type fakeStub struct {
	shim.ChaincodeStubInterface
	state     map[string][]byte
	pending   map[string][]byte // this transaction's writes, invisible to its own reads
	private   map[string][]byte
	transient map[string][]byte
	callerMSP string
	now       int64
	txID      string
	eventName string
}

// As on a real ledger, a read returns committed state, not this transaction's writes.
func (s *fakeStub) GetState(key string) ([]byte, error) { return s.state[key], nil }
func (s *fakeStub) PutState(key string, v []byte) error { s.pending[key] = v; return nil }
func (s *fakeStub) GetTxID() string                     { return s.txID }
func (s *fakeStub) GetTxTimestamp() (*timestamppb.Timestamp, error) {
	return &timestamppb.Timestamp{Seconds: s.now}, nil
}
func (s *fakeStub) SetEvent(name string, _ []byte) error     { s.eventName = name; return nil }
func (s *fakeStub) GetTransient() (map[string][]byte, error) { return s.transient, nil }
func (s *fakeStub) PutPrivateData(collection, key string, v []byte) error {
	s.pending["private/"+collection+"/"+key] = v
	return nil
}
func (s *fakeStub) GetPrivateData(collection, key string) ([]byte, error) {
	member := s.callerMSP == InvestigatorMSP || collectionOf[s.callerMSP] == collection
	if !member {
		return nil, fmt.Errorf("tx creator does not have read access permission on privatedata in collection %s", collection)
	}
	return s.private[collection+"/"+key], nil
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

const (
	bankA   = "BankAOrgMSP"
	bankB   = "BankBOrgMSP"
	sponsor = "SponsorBankOrgMSP"

	victim1 = "1111111111111111111111111111111111111111111111111111111111111111"
	victim2 = "2222222222222222222222222222222222222222222222222222222222222222"
	mule    = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	second  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

var collectionOf = map[string]string{bankA: "caseBankA", bankB: "caseBankB", sponsor: "caseSponsorBank"}

type world struct {
	t    *testing.T
	stub *fakeStub
	cc   *FraudHoldContract
	n    int
}

// tx runs fn as one transaction submitted by caller and simulated on peer's
// org. Its writes reach the ledger only if fn succeeds.
func (w *world) tx(caller, peer string, details string, fn func(ctx *fakeCtx) error) error {
	w.n++
	w.stub.txID = fmt.Sprintf("tx%04d", w.n)
	w.stub.eventName = ""
	w.stub.pending = map[string][]byte{}
	w.stub.callerMSP = caller
	w.stub.transient = map[string][]byte{}
	if details != "" {
		w.stub.transient["details"] = []byte(details)
	}
	peerMSPID = func() (string, error) { return peer, nil }
	err := fn(&fakeCtx{stub: w.stub, id: &fakeID{msp: caller}})
	if err == nil {
		for k, v := range w.stub.pending {
			if strings.HasPrefix(k, "private/") {
				w.stub.private[strings.TrimPrefix(k, "private/")] = v
			} else {
				w.stub.state[k] = v
			}
		}
	}
	return err
}

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

func (w *world) complaint(id string) *Complaint {
	w.t.Helper()
	var c *Complaint
	w.must(w.tx(InvestigatorMSP, InvestigatorMSP, "", func(ctx *fakeCtx) (err error) { c, err = w.cc.GetComplaint(ctx, id); return }))
	return c
}

func (w *world) hold(caller, peer, id, complaint, rrn string, amount int64, provision, caseRef string) error {
	return w.tx(caller, peer, "", func(ctx *fakeCtx) error {
		_, err := w.cc.PlaceHold(ctx, id, complaint, rrn, amount, provision, caseRef)
		return err
	})
}

// setup: victim 1 at Bank A lost Rs 60,000 and victim 2 at the sponsor bank
// lost Rs 40,000, both to one mule account at Bank B. Rs 30,000 of victim 1's
// money was then moved on to a second account at Bank A.
func setup(t *testing.T) *world {
	w := &world{t: t, cc: &FraudHoldContract{},
		stub: &fakeStub{state: map[string][]byte{}, private: map[string][]byte{}, now: 3_000_000}}
	for _, b := range [][2]string{{"BANKA", bankA}, {"BANKB", bankB}, {"SPONSORBANK", sponsor}} {
		w.must(w.tx(InvestigatorMSP, InvestigatorMSP, "", func(ctx *fakeCtx) error {
			return w.cc.RegisterBank(ctx, b[0], b[1], collectionOf[b[1]])
		}))
	}
	w.must(w.tx(bankA, bankA, `{"victim":"A. Kumar"}`, func(ctx *fakeCtx) error {
		_, err := w.cc.FileComplaint(ctx, "CMP-1", "BANKA", victim1, 6_000_000, `["RRN-V1-PAY"]`, "NCRP-2026-0001")
		return err
	}))
	w.must(w.tx(sponsor, sponsor, `{"victim":"S. Devi"}`, func(ctx *fakeCtx) error {
		_, err := w.cc.FileComplaint(ctx, "CMP-2", "SPONSORBANK", victim2, 4_000_000, `["RRN-V2-PAY"]`, "NCRP-2026-0002")
		return err
	}))
	rec := func(caller, rrn, parent, fromBank, from, toBank, to string, amount int64) {
		t.Helper()
		w.must(w.tx(caller, caller, "", func(ctx *fakeCtx) error {
			_, err := w.cc.RecordTransfer(ctx, rrn, parent, fromBank, from, toBank, to, amount)
			return err
		}))
	}
	rec(bankB, "RRN-V1-PAY", "", "BANKA", victim1, "BANKB", mule, 6_000_000)
	rec(bankB, "RRN-V2-PAY", "", "SPONSORBANK", victim2, "BANKB", mule, 4_000_000)
	rec(bankA, "RRN-ONWARD", "RRN-V1-PAY", "BANKB", mule, "BANKA", second, 3_000_000)
	return w
}

// ---- tests ------------------------------------------------------------------

func TestProrata(t *testing.T) {
	cases := []struct {
		name   string
		total  int64
		claims []claim
		want   []int64
	}{
		{"60:40 of 50,000", 5_000_000, []claim{{6_000_000, 6_000_000}, {4_000_000, 4_000_000}}, []int64{3_000_000, 2_000_000}},
		{"everything recovered", 10, []claim{{6, 6}, {4, 4}}, []int64{6, 4}},
		{"rounding paise go to the largest remainder", 100, []claim{{1, 100}, {1, 100}, {1, 100}}, []int64{34, 33, 33}},
		{"a capped victim's excess goes to the others", 5_000_000, []claim{{6_000_000, 2_000_000}, {4_000_000, 4_000_000}}, []int64{2_000_000, 3_000_000}},
		{"one victim", 700, []claim{{1000, 900}}, []int64{700}},
	}
	for _, c := range cases {
		got := prorata(c.total, c.claims)
		var sum int64
		for i, g := range got {
			sum += g
			if g != c.want[i] || g > c.claims[i].cap {
				t.Errorf("%s: got %v, want %v", c.name, got, c.want)
				break
			}
		}
		if sum != c.total {
			t.Errorf("%s: shares %v add up to %d, not %d", c.name, got, sum, c.total)
		}
	}
}

func TestTrace(t *testing.T) {
	w := setup(t)
	var rows []*TraceRow
	w.must(w.tx(InvestigatorMSP, InvestigatorMSP, "", func(ctx *fakeCtx) (err error) { rows, err = w.cc.GetTrace(ctx, "CMP-1"); return }))
	if len(rows) != 2 {
		t.Fatalf("expected two hops for CMP-1, got %d", len(rows))
	}
	// Rs 60,000 arrived at the mule; Rs 30,000 moved on, Rs 30,000 is still there.
	if r := rows[0]; r.RRN != "RRN-V1-PAY" || r.Tainted != 6_000_000 || r.MovedOn != 3_000_000 || r.AvailableToHold != 3_000_000 {
		t.Fatalf("unexpected first hop: %+v", r)
	}
	if r := rows[1]; r.RRN != "RRN-ONWARD" || r.Depth != 2 || r.ToBank != "BANKA" || r.AvailableToHold != 3_000_000 {
		t.Fatalf("unexpected second hop: %+v", r)
	}
}

func TestTransferRules(t *testing.T) {
	w := setup(t)
	rec := func(caller, peer, rrn, parent, fromBank, from, toBank, to string, amount int64) error {
		return w.tx(caller, peer, "", func(ctx *fakeCtx) error {
			_, err := w.cc.RecordTransfer(ctx, rrn, parent, fromBank, from, toBank, to, amount)
			return err
		})
	}
	w.wantErr(rec(bankA, bankA, "RRN-NEW-01", "", "BANKA", victim1, "BANKB", mule, 100), "recorded by the bank that received it")
	w.wantErr(rec(bankB, bankB, "RRN-NEW-01", "", "BANKA", victim1, "BANKB", mule, 100), "no complaint cites payment")
	w.wantErr(rec(bankB, bankB, "RRN-V1-PAY", "", "BANKA", victim1, "BANKB", mule, 100), "already recorded")
	// Money cannot be traced onward from an account it never arrived in.
	w.wantErr(rec(bankA, bankA, "RRN-NEW-02", "RRN-V1-PAY", "BANKB", second, "BANKA", second, 100), "does not leave the account")
	// A bigger onward transfer carries only what was left of the victim's money.
	w.must(rec(bankA, bankA, "RRN-NEW-03", "RRN-V1-PAY", "BANKB", mule, "BANKA", second, 9_000_000))
	var tr *Transfer
	w.must(w.tx(InvestigatorMSP, InvestigatorMSP, "", func(ctx *fakeCtx) (err error) {
		tr, err = load[Transfer](ctx, prefixTransfer+"RRN-NEW-03", "transfer")
		return
	}))
	if tr.Tainted != 3_000_000 {
		t.Fatalf("only Rs 30,000 of victim money was left to move on, got %d", tr.Tainted)
	}
	w.wantErr(rec(bankA, bankA, "RRN-NEW-04", "RRN-V1-PAY", "BANKB", mule, "BANKA", second, 100), "none of the victims' money")
}

func TestHoldNeedsLegalBasis(t *testing.T) {
	w := setup(t)
	w.wantErr(w.hold(InvestigatorMSP, bankB, "HOLD-1", "CMP-1", "RRN-V1-PAY", 3_000_000, "", "NCRP-2026-0001"), "no legal provision cited")
	w.wantErr(w.hold(InvestigatorMSP, bankB, "HOLD-1", "CMP-1", "RRN-V1-PAY", 3_000_000, "BNSS-106", ""), "no case reference given")
	w.wantErr(w.hold(InvestigatorMSP, bankB, "HOLD-1", "CMP-1", "RRN-V1-PAY", 3_000_000, "BECAUSE-I-SAY-SO", "NCRP-2026-0001"), "not an accepted legal provision")
	w.wantErr(w.hold(InvestigatorMSP, bankB, "HOLD-1", "CMP-1", "RRN-V1-PAY", 3_000_000, "BNSS-106", "NCRP-2026-9999"), "does not match complaint")
	w.must(w.hold(InvestigatorMSP, bankB, "HOLD-1", "CMP-1", "RRN-V1-PAY", 3_000_000, "BNSS-106", "NCRP-2026-0001"))
	if w.stub.eventName != EventHoldPlaced {
		t.Fatalf("a hold should emit %s", EventHoldPlaced)
	}
}

func TestHoldIsLimitedToTheDisputedAmount(t *testing.T) {
	w := setup(t)
	// Only Rs 30,000 of victim 1's money is still in the mule account.
	w.wantErr(w.hold(InvestigatorMSP, bankB, "HOLD-1", "CMP-1", "RRN-V1-PAY", 6_000_000, "BNSS-106", "NCRP-2026-0001"), "only Rs 30000.00 of the victims' money")
	// Victim 1's complaint cannot hold victim 2's money.
	w.wantErr(w.hold(InvestigatorMSP, bankB, "HOLD-1", "CMP-1", "RRN-V2-PAY", 1_000_000, "BNSS-106", "NCRP-2026-0001"), "is not traced from complaint CMP-1")
	w.must(w.hold(InvestigatorMSP, bankB, "HOLD-1", "CMP-1", "RRN-V1-PAY", 3_000_000, "BNSS-106", "NCRP-2026-0001"))
	w.must(w.hold(InvestigatorMSP, bankA, "HOLD-2", "CMP-1", "RRN-ONWARD", 3_000_000, "BNSS-106", "NCRP-2026-0001"))
	if c := w.complaint("CMP-1"); c.Held != 6_000_000 {
		t.Fatalf("CMP-1 should have its full Rs 60,000 held across two banks, got %d", c.Held)
	}
}

func TestHoldEndorsedOnlyByHoldingBankAndInvestigator(t *testing.T) {
	w := setup(t)
	// The money is at Bank B. Bank A's peer, or any other org's, refuses to endorse.
	for _, peer := range []string{bankA, sponsor, "SwitchOrgMSP", "AggregatorOrgMSP"} {
		w.wantErr(w.hold(InvestigatorMSP, peer, "HOLD-1", "CMP-1", "RRN-V1-PAY", 3_000_000, "BNSS-106", "NCRP-2026-0001"),
			"must be endorsed by InvestigatorOrgMSP and BankBOrgMSP")
	}
	// Another bank cannot ask for a hold on an account it does not hold.
	w.wantErr(w.hold(bankA, bankB, "HOLD-1", "CMP-1", "RRN-V1-PAY", 3_000_000, "BNSS-106", "NCRP-2026-0001"), "can be requested by the investigating authority or BankBOrgMSP")
	// Both the Investigator's peer and Bank B's peer accept it.
	w.must(w.hold(bankB, InvestigatorMSP, "HOLD-1", "CMP-1", "RRN-V1-PAY", 1_000_000, "BNSS-106", "NCRP-2026-0001"))
	w.must(w.hold(InvestigatorMSP, bankB, "HOLD-2", "CMP-1", "RRN-V1-PAY", 1_000_000, "BNSS-106", "NCRP-2026-0001"))
}

func TestDuplicateHoldFromAnIndependentComplaint(t *testing.T) {
	w := setup(t)
	w.must(w.hold(InvestigatorMSP, bankB, "HOLD-1", "CMP-1", "RRN-V1-PAY", 3_000_000, "BNSS-106", "NCRP-2026-0001"))

	// The same loss is reported again through another channel, citing the same payment.
	var dup *Complaint
	w.must(w.tx(bankA, bankA, `{"victim":"A. Kumar","channel":"helpline"}`, func(ctx *fakeCtx) (err error) {
		dup, err = w.cc.FileComplaint(ctx, "CMP-3", "BANKA", victim1, 6_000_000, `["RRN-V1-PAY"]`, "NCRP-2026-0003")
		return
	}))
	if strings.Join(dup.OverlapsWith, ",") != "CMP-1" {
		t.Fatalf("the second complaint should be flagged as overlapping CMP-1: %+v", dup.OverlapsWith)
	}
	err := w.hold(InvestigatorMSP, bankB, "HOLD-3", "CMP-3", "RRN-V1-PAY", 3_000_000, "BNSS-106", "NCRP-2026-0003")
	w.wantErr(err, "duplicate hold refused: Rs 30000.00 of the funds from RRN-V1-PAY are already held under HOLD-1")
	var tr *Transfer
	w.must(w.tx(InvestigatorMSP, InvestigatorMSP, "", func(ctx *fakeCtx) (err error) {
		tr, err = load[Transfer](ctx, prefixTransfer+"RRN-V1-PAY", "transfer")
		return
	}))
	if tr.Held != 3_000_000 || len(tr.HoldIDs) != 1 {
		t.Fatalf("the funds must stay held exactly once: %+v", tr)
	}
}

func TestReleaseHold(t *testing.T) {
	w := setup(t)
	w.must(w.hold(InvestigatorMSP, bankB, "HOLD-1", "CMP-1", "RRN-V1-PAY", 3_000_000, "BNSS-106", "NCRP-2026-0001"))
	rel := func(caller, peer, reason string) error {
		return w.tx(caller, peer, "", func(ctx *fakeCtx) error { _, err := w.cc.ReleaseHold(ctx, "HOLD-1", reason); return err })
	}
	w.wantErr(rel(bankB, bankB, "customer asked"), "only the investigating authority")
	w.wantErr(rel(InvestigatorMSP, bankB, " "), "a reason is required")
	w.must(rel(InvestigatorMSP, bankB, "complaint withdrawn"))
	w.wantErr(rel(InvestigatorMSP, bankB, "again"), "not active")
	if c := w.complaint("CMP-1"); c.Held != 0 {
		t.Fatalf("release should free the complaint's held amount, got %d", c.Held)
	}
	// The funds can be held again.
	w.must(w.hold(InvestigatorMSP, bankB, "HOLD-1B", "CMP-1", "RRN-V1-PAY", 3_000_000, "BNSS-106", "NCRP-2026-0001"))
}

func TestProRataRecoveryAndRepayment(t *testing.T) {
	w := setup(t)
	// Two holds for victim 1 on the same transfer, one for victim 2.
	w.must(w.hold(InvestigatorMSP, bankB, "HOLD-1A", "CMP-1", "RRN-V1-PAY", 1_000_000, "BNSS-106", "NCRP-2026-0001"))
	w.must(w.hold(InvestigatorMSP, bankB, "HOLD-1B", "CMP-1", "RRN-V1-PAY", 2_000_000, "BNSS-106", "NCRP-2026-0001"))
	w.must(w.hold(InvestigatorMSP, bankB, "HOLD-2", "CMP-2", "RRN-V2-PAY", 4_000_000, "BNSS-107", "NCRP-2026-0002"))

	order := func(caller, peer, id string, amount int64, ref string) (*Order, error) {
		var o *Order
		err := w.tx(caller, peer, "", func(ctx *fakeCtx) (err error) {
			o, err = w.cc.OrderDisbursal(ctx, id, "BANKB", mule, amount, ref)
			return
		})
		return o, err
	}
	_, err := order(bankB, bankB, "ORD-1", 5_000_000, "CRT-2026-77")
	w.wantErr(err, "only the investigating authority")
	_, err = order(InvestigatorMSP, bankA, "ORD-1", 5_000_000, "CRT-2026-77")
	w.wantErr(err, "must be endorsed by InvestigatorOrgMSP and BankBOrgMSP")
	_, err = order(InvestigatorMSP, bankB, "ORD-1", 5_000_000, "")
	w.wantErr(err, "court order reference are required")
	_, err = order(InvestigatorMSP, bankB, "ORD-1", 7_000_001, "CRT-2026-77")
	w.wantErr(err, "between Rs 0.01 and the Rs 70000.00 held")

	// Rs 70,000 is held but only Rs 50,000 is recovered: split 60:40 by what each lost.
	o, err := order(InvestigatorMSP, bankB, "ORD-1", 5_000_000, "CRT-2026-77")
	w.must(err)
	if o.TotalHeld != 7_000_000 || o.ReleasedToAccount != 2_000_000 || len(o.Shares) != 2 {
		t.Fatalf("unexpected order: %+v", o)
	}
	if o.Shares[0].ComplaintID != "CMP-1" || o.Shares[0].Amount != 3_000_000 || o.Shares[1].Amount != 2_000_000 {
		t.Fatalf("expected Rs 30,000 and Rs 20,000, got %+v", o.Shares)
	}
	_, err = order(InvestigatorMSP, bankB, "ORD-1", 5_000_000, "CRT-2026-77")
	w.wantErr(err, "duplicate: order ORD-1")
	_, err = order(InvestigatorMSP, bankB, "ORD-2", 1, "CRT-2026-78")
	w.wantErr(err, "no active hold")

	// Both holds on the one transfer were closed, and nothing of it is left to hold.
	var tr *Transfer
	w.must(w.tx(InvestigatorMSP, InvestigatorMSP, "", func(ctx *fakeCtx) (err error) {
		tr, err = load[Transfer](ctx, prefixTransfer+"RRN-V1-PAY", "transfer")
		return
	}))
	if tr.Held != 0 || tr.available() != 0 {
		t.Fatalf("transfer should have nothing held or holdable: %+v", tr)
	}

	// Each victim's own bank confirms it has repaid them.
	confirm := func(caller, complaint string) (*Order, error) {
		var o *Order
		err := w.tx(caller, bankB, "", func(ctx *fakeCtx) (err error) {
			o, err = w.cc.ConfirmRepayment(ctx, "ORD-1", complaint)
			return
		})
		return o, err
	}
	_, err = confirm(bankB, "CMP-1")
	w.wantErr(err, "only the victim's bank")
	o, err = confirm(bankA, "CMP-1")
	w.must(err)
	if o.Status != OrderPending {
		t.Fatalf("order should wait for the second victim's bank, got %s", o.Status)
	}
	_, err = confirm(bankA, "CMP-1")
	w.wantErr(err, "duplicate: repayment")
	o, err = confirm(sponsor, "CMP-2")
	w.must(err)
	if o.Status != OrderComplete {
		t.Fatalf("order should be complete, got %s", o.Status)
	}
	if c := w.complaint("CMP-1"); c.Recovered != 3_000_000 || c.Held != 0 {
		t.Fatalf("CMP-1 should show Rs 30,000 recovered and nothing held: %+v", c)
	}
	// Victim 1 can still recover the Rs 30,000 that moved on to Bank A.
	w.must(w.hold(InvestigatorMSP, bankA, "HOLD-9", "CMP-1", "RRN-ONWARD", 3_000_000, "BNSS-106", "NCRP-2026-0001"))
	// But not more than is still owed to them.
	w.wantErr(w.hold(InvestigatorMSP, bankA, "HOLD-10", "CMP-1", "RRN-ONWARD", 1, "BNSS-106", "NCRP-2026-0001"), "refused")
}

func TestComplaintRules(t *testing.T) {
	w := setup(t)
	file := func(caller, peer, id, bank, details string, rrns, caseRef string) error {
		return w.tx(caller, peer, details, func(ctx *fakeCtx) error {
			_, err := w.cc.FileComplaint(ctx, id, bank, victim1, 100, rrns, caseRef)
			return err
		})
	}
	w.wantErr(file(bankB, bankB, "CMP-9", "BANKA", `{}`, `["RRN-XYZ-01"]`, "NCRP-9"), "filed by the victim's own bank")
	w.wantErr(file(bankA, bankB, "CMP-9", "BANKA", `{}`, `["RRN-XYZ-01"]`, "NCRP-9"), "must be endorsed by InvestigatorOrgMSP and BankAOrgMSP")
	w.wantErr(file(bankA, bankA, "CMP-9", "BANKA", `{}`, `["RRN-XYZ-01"]`, ""), "a case reference is required")
	w.wantErr(file(bankA, bankA, "CMP-9", "BANKA", `{}`, `[]`, "NCRP-9"), "1 to 20 payment references")
	w.wantErr(file(bankA, bankA, "CMP-9", "BANKA", ``, `["RRN-XYZ-01"]`, "NCRP-9"), "must be sent as transient data")
	w.wantErr(file(bankA, bankA, "CMP-1", "BANKA", `{}`, `["RRN-XYZ-01"]`, "NCRP-9"), "already filed")
}

func TestCaseDetailsArePrivate(t *testing.T) {
	w := setup(t)
	read := func(caller string) (string, error) {
		var out string
		err := w.tx(caller, caller, "", func(ctx *fakeCtx) (err error) { out, err = w.cc.GetCaseDetails(ctx, "complaint", "CMP-1"); return })
		return out, err
	}
	for _, ok := range []string{InvestigatorMSP, bankA} {
		out, err := read(ok)
		w.must(err)
		if !strings.Contains(out, "A. Kumar") {
			t.Fatalf("%s should read the victim's details, got %q", ok, out)
		}
	}
	// The bank holding the mule account is not party to the victim's details.
	for _, no := range []string{bankB, sponsor, "SwitchOrgMSP"} {
		_, err := read(no)
		w.wantErr(err, "not available to this org")
	}
	for k, v := range w.stub.state {
		if strings.Contains(string(v), "Kumar") || strings.Contains(string(v), "Devi") {
			t.Errorf("public key %s leaks a victim's name", k)
		}
	}
}

func TestProvisions(t *testing.T) {
	w := setup(t)
	set := func(caller, list string) error {
		return w.tx(caller, InvestigatorMSP, "", func(ctx *fakeCtx) error { return w.cc.SetProvisions(ctx, list) })
	}
	w.wantErr(set(bankB, `["X-1"]`), "only the investigating authority")
	w.wantErr(set(InvestigatorMSP, `[]`), "non-empty JSON array")
	w.must(set(InvestigatorMSP, `["BNSS-106"]`))
	w.wantErr(w.hold(InvestigatorMSP, bankB, "HOLD-1", "CMP-2", "RRN-V2-PAY", 100, "BNSS-107", "NCRP-2026-0002"), "not an accepted legal provision")
	w.must(w.hold(InvestigatorMSP, bankB, "HOLD-1", "CMP-2", "RRN-V2-PAY", 100, "BNSS-106", "NCRP-2026-0002"))
}
