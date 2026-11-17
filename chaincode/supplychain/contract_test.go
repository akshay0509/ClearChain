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
	private   map[string][]byte
	transient map[string][]byte
	// member is false when the calling org is outside the private collection.
	member    bool
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
func (s *fakeStub) GetTransient() (map[string][]byte, error) { return s.transient, nil }
func (s *fakeStub) GetPrivateData(collection, key string) ([]byte, error) {
	if !s.member {
		return nil, fmt.Errorf("tx creator does not have read access permission on privatedata in collection %s", collection)
	}
	return s.private[collection+"/"+key], nil
}
func (s *fakeStub) PutPrivateData(collection, key string, v []byte) error {
	s.private[collection+"/"+key] = v
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
	msp, id string
}

func (f *fakeID) GetMSPID() (string, error) { return f.msp, nil }
func (f *fakeID) GetID() (string, error)    { return f.id, nil }

type fakeCtx struct {
	contractapi.TransactionContextInterface
	stub *fakeStub
	id   *fakeID
}

func (c *fakeCtx) GetStub() shim.ChaincodeStubInterface  { return c.stub }
func (c *fakeCtx) GetClientIdentity() cid.ClientIdentity { return c.id }

// who is a signing identity: an org and a certificate.
type who struct{ msp, cert string }

var (
	buyerUser = who{BuyerMSP, "x509::CN=User1@buyer"}
	t1User    = who{"BankAOrgMSP", "x509::CN=User2@banka"}
	t2User    = who{"BankBOrgMSP", "x509::CN=User2@bankb"}
	t3User    = who{"BankAOrgMSP", "x509::CN=User3@banka"}
	stranger  = who{"BankBOrgMSP", "x509::CN=User3@bankb"}
	outsider  = who{"SwitchOrgMSP", "x509::CN=User1@switch"}
)

const (
	hashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	t0    = int64(1_800_000_000)
)

type world struct {
	t    *testing.T
	stub *fakeStub
	cc   *SupplyChainContract
	n    int
}

// as prepares a transaction signed by w. amount > 0 attaches private terms.
func (w *world) as(u who, amount int64, invoice string) *fakeCtx {
	w.n++
	w.stub.txID = fmt.Sprintf("tx%04d", w.n)
	w.stub.eventName, w.stub.event = "", nil
	w.stub.transient = map[string][]byte{}
	if amount != 0 || invoice != "" {
		w.stub.transient["terms"] = []byte(fmt.Sprintf(`{"amount":%d,"invoiceRef":%q}`, amount, invoice))
	}
	w.stub.member = u.msp == BuyerMSP || u.msp == "BankAOrgMSP" || u.msp == "BankBOrgMSP"
	return &fakeCtx{stub: w.stub, id: &fakeID{msp: u.msp, id: u.cert}}
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

func setup(t *testing.T) *world {
	w := &world{t: t, cc: &SupplyChainContract{},
		stub: &fakeStub{state: map[string][]byte{}, private: map[string][]byte{}, now: t0}}
	w.must(w.cc.RegisterParty(w.as(buyerUser, 0, ""), "ANCHOR", "Anchor Motors", RoleBuyer, "", "", "BANKA", hashA))
	w.must(w.cc.RegisterParty(w.as(t1User, 0, ""), "TIER1", "Precision Castings", RoleSupplier, "UDYAM-MH-12-0000001", ClassMedium, "BANKA", hashA))
	w.must(w.cc.RegisterParty(w.as(t2User, 0, ""), "TIER2", "Shree Forgings", RoleSupplier, "UDYAM-MH-12-0000002", ClassSmall, "BANKB", hashA))
	w.must(w.cc.RegisterParty(w.as(t3User, 0, ""), "TIER3", "Raju Tooling", RoleSupplier, "UDYAM-MH-12-0000003", ClassMicro, "BANKA", hashA))
	return w
}

// chain builds buyer -> TIER1 (10,00,000) -> TIER2 (6,00,000) -> TIER3 (2,50,000), all approved.
func (w *world) chain() {
	w.t.Helper()
	_, err := w.cc.CreateChain(w.as(buyerUser, 100_000_000, "INV-A-1"), "CH1", "OB1", "TIER1", t0, 30, t0+30*day)
	w.must(err)
	_, err = w.cc.RegisterObligation(w.as(t1User, 60_000_000, "INV-T1-9"), "OB2", "CH1", "TIER2", t0, 30, t0+30*day)
	w.must(err)
	_, err = w.cc.ApproveObligation(w.as(buyerUser, 0, ""), "OB2")
	w.must(err)
	_, err = w.cc.RegisterObligation(w.as(t2User, 25_000_000, "INV-T2-4"), "OB3", "CH1", "TIER3", t0, 0, t0+15*day)
	w.must(err)
	_, err = w.cc.ApproveObligation(w.as(t1User, 0, ""), "OB3")
	w.must(err)
}

// ---- tests ------------------------------------------------------------------

func TestStatutoryDue(t *testing.T) {
	cases := []struct {
		name     string
		agreed   int
		class    string
		covered  bool
		wantDays int64
	}{
		{"micro, nothing agreed in writing", 0, ClassMicro, true, 15},
		{"small, 30 days agreed", 30, ClassSmall, true, 30},
		{"small, 90 days agreed is capped at 45", 90, ClassSmall, true, 45},
		{"medium enterprises are not covered", 30, ClassMedium, false, 0},
	}
	for _, c := range cases {
		covered, due := statutoryDue(t0, c.agreed, c.class)
		if covered != c.covered || (covered && due != t0+c.wantDays*day) {
			t.Errorf("%s: covered=%v due=+%d days", c.name, covered, (due-t0)/day)
		}
	}
}

func TestDeadlineFlag(t *testing.T) {
	due := t0 + 15*day
	cases := []struct {
		asOf     int64
		wantFlag string
		wantDays int64
	}{
		{t0, FlagOK, 15},
		{t0 + 12*day, FlagDueSoon, 3},
		{due, FlagDueSoon, 0},
		{due + 1, FlagOverdue, -1},
		{due + 3*day, FlagOverdue, -3},
	}
	for _, c := range cases {
		flag, days := deadlineFlag(true, due, c.asOf, 3)
		if flag != c.wantFlag || days != c.wantDays {
			t.Errorf("asOf +%ds: got %s %d, want %s %d", c.asOf-t0, flag, days, c.wantFlag, c.wantDays)
		}
	}
	if flag, _ := deadlineFlag(false, 0, t0, 3); flag != FlagNotCovered {
		t.Errorf("uncovered obligation should be %s, got %s", FlagNotCovered, flag)
	}
}

func TestPartyRegistration(t *testing.T) {
	w := setup(t)
	reg := func(u who, id, role, udyam, class, h string) error {
		return w.cc.RegisterParty(w.as(u, 0, ""), id, "Name", role, udyam, class, "BANKB", h)
	}
	w.wantErr(reg(stranger, "BUYER2", RoleBuyer, "", "", hashA), "a buyer must act through BuyerOrgMSP")
	w.wantErr(reg(stranger, "S9", RoleSupplier, "12345", ClassMicro, hashA), "udyam must look like")
	w.wantErr(reg(stranger, "S9", RoleSupplier, "UDYAM-MH-12-0000009", "LARGE", hashA), "class must be")
	w.wantErr(reg(stranger, "S9", RoleSupplier, "UDYAM-MH-12-0000009", ClassMicro, "ACC123"), "never stored")
	w.wantErr(reg(stranger, "TIER1", RoleSupplier, "UDYAM-MH-12-0000009", ClassMicro, hashA), "already registered")
	// One identity cannot hold two places.
	w.wantErr(reg(t1User, "S9", RoleSupplier, "UDYAM-MH-12-0000009", ClassMicro, hashA), "already registered as party TIER1")
	w.must(reg(stranger, "S9", RoleSupplier, "UDYAM-MH-12-0000009", ClassMicro, hashA))
}

func TestOnlyTheDebtorCanCommitItsOwnMoney(t *testing.T) {
	w := setup(t)
	_, err := w.cc.CreateChain(w.as(t1User, 100, "INV"), "CH1", "OB1", "TIER2", t0, 30, t0+30*day)
	w.wantErr(err, "only a buyer can open a chain")
	_, err = w.cc.CreateChain(w.as(outsider, 100, "INV"), "CH1", "OB1", "TIER1", t0, 30, t0+30*day)
	w.wantErr(err, "not registered as a party")
	_, err = w.cc.CreateChain(w.as(buyerUser, 0, ""), "CH1", "OB1", "TIER1", t0, 30, t0+30*day)
	w.wantErr(err, "must be sent as transient data")
	_, err = w.cc.CreateChain(w.as(buyerUser, 100_000_000, "INV-A-1"), "CH1", "OB1", "TIER1", t0, 30, t0+30*day)
	w.must(err)

	// TIER2 is not in the chain yet, so it has nothing to pass down.
	_, err = w.cc.RegisterObligation(w.as(t2User, 100, "INV"), "OB9", "CH1", "TIER3", t0, 0, t0+15*day)
	w.wantErr(err, "TIER2 is not owed anything in chain CH1")
	// The buyer cannot register TIER1's downstream obligation for it.
	_, err = w.cc.RegisterObligation(w.as(buyerUser, 100, "INV"), "OB9", "CH1", "TIER2", t0, 0, t0+15*day)
	w.wantErr(err, "only suppliers register downstream obligations")

	_, err = w.cc.RegisterObligation(w.as(t1User, 60_000_000, "INV-T1-9"), "OB2", "CH1", "TIER2", t0, 30, t0+30*day)
	w.must(err)
	// TIER2's place is not approved yet, so it cannot pass anything down.
	_, err = w.cc.RegisterObligation(w.as(t2User, 100, "INV"), "OB3", "CH1", "TIER3", t0, 0, t0+15*day)
	w.wantErr(err, "has not been approved yet")
}

func TestApprovalComesFromTheTierAbove(t *testing.T) {
	w := setup(t)
	_, err := w.cc.CreateChain(w.as(buyerUser, 100_000_000, "INV-A-1"), "CH1", "OB1", "TIER1", t0, 30, t0+30*day)
	w.must(err)
	ob, err := w.cc.RegisterObligation(w.as(t1User, 60_000_000, "INV-T1-9"), "OB2", "CH1", "TIER2", t0, 30, t0+30*day)
	w.must(err)
	if ob.Status != ObRegistered || ob.ApproverID != "ANCHOR" || ob.Level != 2 || ob.DebtorID != "TIER1" {
		t.Fatalf("unexpected obligation: %+v", ob)
	}
	// Neither the debtor, nor the creditor, nor a bystander can approve.
	for _, u := range []who{t1User, t2User, t3User} {
		_, err = w.cc.ApproveObligation(w.as(u, 0, ""), "OB2")
		w.wantErr(err, "can only be approved by ANCHOR")
	}
	_, err = w.cc.ApproveObligation(w.as(buyerUser, 0, ""), "OB2")
	w.must(err)
	_, err = w.cc.ApproveObligation(w.as(buyerUser, 0, ""), "OB2")
	w.wantErr(err, "not awaiting approval")
	// The buyer's own obligation needs no approval and cannot be "approved".
	_, err = w.cc.ApproveObligation(w.as(buyerUser, 0, ""), "OB1")
	w.wantErr(err, "can only be approved by")
}

func TestCannotPassDownMoreThanOwed(t *testing.T) {
	w := setup(t)
	w.must(w.cc.RegisterParty(w.as(stranger, 0, ""), "TIER2B", "Second Forge", RoleSupplier, "UDYAM-MH-12-0000008", ClassMicro, "BANKB", hashA))
	_, err := w.cc.CreateChain(w.as(buyerUser, 100_000_000, "INV-A-1"), "CH1", "OB1", "TIER1", t0, 30, t0+30*day)
	w.must(err)
	_, err = w.cc.RegisterObligation(w.as(t1User, 100_000_001, "INV"), "OB2", "CH1", "TIER2", t0, 30, t0+30*day)
	w.wantErr(err, "cannot pass down")
	_, err = w.cc.RegisterObligation(w.as(t1User, 60_000_000, "INV-T1-9"), "OB2", "CH1", "TIER2", t0, 30, t0+30*day)
	w.must(err)
	// A second supplier of TIER1: the two together cannot exceed what TIER1 is owed.
	_, err = w.cc.RegisterObligation(w.as(t1User, 40_000_001, "INV-T1-10"), "OB2B", "CH1", "TIER2B", t0, 0, t0+15*day)
	w.wantErr(err, "has already registered 60000000")
	_, err = w.cc.RegisterObligation(w.as(t1User, 40_000_000, "INV-T1-10"), "OB2B", "CH1", "TIER2B", t0, 0, t0+15*day)
	w.must(err)
	// A party holds one place in a chain.
	_, err = w.cc.RegisterObligation(w.as(t1User, 1, "INV-T1-11"), "OB2C", "CH1", "TIER2", t0, 0, t0+15*day)
	w.wantErr(err, "already has a place in chain")
}

func TestReleasePaysEveryApprovedTierInOneTransaction(t *testing.T) {
	w := setup(t)
	w.chain()

	_, err := w.cc.ReleasePayment(w.as(t1User, 0, ""), "CH1", "UTR-777")
	w.wantErr(err, "only ANCHOR, the buyer of chain CH1")

	w.stub.now = t0 + 10*day
	chain, err := w.cc.ReleasePayment(w.as(buyerUser, 0, ""), "CH1", "UTR-777")
	w.must(err)
	releaseTx := w.stub.txID
	if w.stub.eventName != EventReleased {
		t.Fatalf("release should emit %s", EventReleased)
	}
	if strings.Contains(string(w.stub.event), "0000") {
		t.Fatalf("the public event must not carry amounts: %s", w.stub.event)
	}
	if chain.Status != ChainReleased || strings.Join(chain.CreditedParties, ",") != "TIER1,TIER2,TIER3" {
		t.Fatalf("unexpected chain after release: %+v", chain)
	}

	rel, err := w.cc.GetRelease(w.as(buyerUser, 0, ""), "CH1")
	w.must(err)
	want := map[string]int64{"TIER1": 40_000_000, "TIER2": 35_000_000, "TIER3": 25_000_000}
	var sum int64
	for _, c := range rel.Credits {
		if c.Net != want[c.PartyID] {
			t.Errorf("%s credited %d, want %d", c.PartyID, c.Net, want[c.PartyID])
		}
		sum += c.Net
	}
	if sum != 100_000_000 || rel.Total != 100_000_000 || rel.TxID != releaseTx {
		t.Fatalf("credits must add up to the buyer's payment in the release transaction: %+v", rel)
	}

	view, err := w.cc.GetChain(w.as(outsider, 0, ""), "CH1")
	w.must(err)
	for _, o := range view.Obligations {
		if o.Status != ObPaid || o.PaidAt != t0+10*day || !o.PaidOnTime {
			t.Errorf("%s should be paid on time in the release: %+v", o.ObligationID, o)
		}
	}

	_, err = w.cc.ReleasePayment(w.as(buyerUser, 0, ""), "CH1", "UTR-778")
	w.wantErr(err, "duplicate: chain CH1 was already released with payment UTR-777")
	_, err = w.cc.RegisterObligation(w.as(t3User, 1, "INV"), "OB4", "CH1", "TIER2", t0, 0, t0+15*day)
	w.wantErr(err, "must be registered before the buyer pays")
}

func TestUnapprovedObligationIsLeftOut(t *testing.T) {
	w := setup(t)
	_, err := w.cc.CreateChain(w.as(buyerUser, 100_000_000, "INV-A-1"), "CH1", "OB1", "TIER1", t0, 30, t0+30*day)
	w.must(err)
	// TIER1 registers, but the buyer never approves.
	_, err = w.cc.RegisterObligation(w.as(t1User, 60_000_000, "INV-T1-9"), "OB2", "CH1", "TIER2", t0, 30, t0+30*day)
	w.must(err)
	_, err = w.cc.ReleasePayment(w.as(buyerUser, 0, ""), "CH1", "UTR-1")
	w.must(err)
	rel, err := w.cc.GetRelease(w.as(buyerUser, 0, ""), "CH1")
	w.must(err)
	if len(rel.Credits) != 1 || rel.Credits[0].PartyID != "TIER1" || rel.Credits[0].Net != 100_000_000 {
		t.Fatalf("without approval all of it goes to TIER1: %+v", rel.Credits)
	}
	ob, err := loadObligation(w.as(buyerUser, 0, ""), "OB2")
	w.must(err)
	if ob.Status != ObExcluded {
		t.Fatalf("unapproved obligation should be EXCLUDED, got %s", ob.Status)
	}
}

func TestWithdraw(t *testing.T) {
	w := setup(t)
	_, err := w.cc.CreateChain(w.as(buyerUser, 100_000_000, "INV-A-1"), "CH1", "OB1", "TIER1", t0, 30, t0+30*day)
	w.must(err)
	_, err = w.cc.RegisterObligation(w.as(t1User, 60_000_000, "INV-T1-9"), "OB2", "CH1", "TIER2", t0, 30, t0+30*day)
	w.must(err)
	w.wantErr(w.cc.WithdrawObligation(w.as(t2User, 0, ""), "OB2"), "only TIER1")
	w.must(w.cc.WithdrawObligation(w.as(t1User, 0, ""), "OB2"))
	// The amount and the supplier's place are free again.
	_, err = w.cc.RegisterObligation(w.as(t1User, 100_000_000, "INV-T1-9B"), "OB2B", "CH1", "TIER2", t0, 30, t0+30*day)
	w.must(err)
	_, err = w.cc.ApproveObligation(w.as(buyerUser, 0, ""), "OB2B")
	w.must(err)
	w.wantErr(w.cc.WithdrawObligation(w.as(t1User, 0, ""), "OB2B"), "can no longer be withdrawn")
}

func TestLatePaymentIsRecorded(t *testing.T) {
	w := setup(t)
	w.chain()
	w.stub.now = t0 + 20*day // OB3 (micro, nothing agreed) was due on day 15
	_, err := w.cc.ReleasePayment(w.as(buyerUser, 0, ""), "CH1", "UTR-9")
	w.must(err)
	ob3, err := loadObligation(w.as(buyerUser, 0, ""), "OB3")
	w.must(err)
	if ob3.PaidOnTime || ob3.DaysLate != 5 {
		t.Fatalf("OB3 was paid 5 days late: %+v", ob3)
	}
	ob2, err := loadObligation(w.as(buyerUser, 0, ""), "OB2")
	w.must(err)
	if !ob2.PaidOnTime {
		t.Fatalf("OB2 (30 days agreed) was paid on time: %+v", ob2)
	}
}

func TestDeadlineView(t *testing.T) {
	w := setup(t)
	w.chain()

	rows, err := w.cc.DeadlineView(w.as(buyerUser, 0, ""), t0+13*day, 3)
	w.must(err)
	got := map[string]string{}
	for _, r := range rows {
		got[r.ObligationID] = fmt.Sprintf("%s/%d/%d", r.Flag, r.DaysRemaining, r.Amount)
	}
	// OB1: creditor is medium, not covered. OB2: small, 30 days. OB3: micro, 15 days.
	want := map[string]string{
		"OB1": "NOT_COVERED/0/100000000",
		"OB2": "OK/17/60000000",
		"OB3": "DUE_SOON/2/25000000",
	}
	for id, v := range want {
		if got[id] != v {
			t.Errorf("%s: got %s, want %s", id, got[id], v)
		}
	}
	if rows[0].ObligationID != "OB3" || !rows[0].DeductionAtRisk {
		t.Errorf("the most urgent obligation should come first and be marked at risk: %+v", rows[0])
	}

	rows, err = w.cc.DeadlineView(w.as(buyerUser, 0, ""), t0+16*day, 3)
	w.must(err)
	if rows[0].ObligationID != "OB3" || rows[0].Flag != FlagOverdue || rows[0].DaysRemaining != -1 {
		t.Errorf("OB3 should be a day overdue: %+v", rows[0])
	}

	// An org outside the collection sees the dates and flags but not the amounts.
	rows, err = w.cc.DeadlineView(w.as(outsider, 0, ""), t0+13*day, 3)
	w.must(err)
	for _, r := range rows {
		if r.Amount != -1 {
			t.Errorf("%s: amount leaked to a non-member: %d", r.ObligationID, r.Amount)
		}
	}

	// Paid obligations drop out of the view.
	_, err = w.cc.ReleasePayment(w.as(buyerUser, 0, ""), "CH1", "UTR-1")
	w.must(err)
	rows, err = w.cc.DeadlineView(w.as(buyerUser, 0, ""), t0+13*day, 3)
	w.must(err)
	if len(rows) != 0 {
		t.Errorf("expected an empty view after release, got %d rows", len(rows))
	}
}

func TestTermsArePrivate(t *testing.T) {
	w := setup(t)
	w.chain()
	terms, err := w.cc.GetTerms(w.as(t3User, 0, ""), "OB3")
	w.must(err)
	if terms.Amount != 25_000_000 || terms.InvoiceRef != "INV-T2-4" {
		t.Fatalf("unexpected terms: %+v", terms)
	}
	_, err = w.cc.GetTerms(w.as(outsider, 0, ""), "OB3")
	w.wantErr(err, "read access permission")
	// Nothing in the public state carries an amount or invoice reference.
	for k, v := range w.stub.state {
		if strings.Contains(string(v), "INV-") || strings.Contains(string(v), "25000000") {
			t.Errorf("public key %s leaks private terms: %s", k, v)
		}
	}
}
