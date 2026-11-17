package main

import (
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strings"

	"github.com/hyperledger/fabric-chaincode-go/v2/shim"
	"github.com/hyperledger/fabric-contract-api-go/v2/contractapi"
)

// FraudHoldContract lets banks and the investigating authority trace fraud
// money across banks and hold exactly the disputed amount where it sits,
// instead of freezing a whole account.
//
//   - The victim's bank files a complaint citing the victim's own payments.
//   - Each bank records the transfers that landed with it, so the money can be
//     followed hop by hop. A transfer carries a "tainted" amount: how much of
//     it is the victim's money.
//   - A hold is placed against one traced transfer, for no more than is still
//     sitting there. It must cite a legal provision and the case reference.
//   - If the same funds are already held under another complaint, a second
//     hold on them is refused as a duplicate.
//   - On a court order the recovered money is split among the victims in
//     proportion to what each lost, and each victim's bank confirms repayment.
//
// Every state change needs the Investigator's peer and one bank's peer to
// endorse (the chaincode policy). Which bank is fixed by the contract: it
// refuses to run on any peer other than the Investigator's and the bank the
// action concerns, so a hold at one bank cannot be endorsed by another.
//
// Names and narratives are case details: they go to a private collection
// shared only by the Investigator and the bank concerned.
type FraudHoldContract struct {
	contractapi.Contract
}

const (
	InvestigatorMSP = "InvestigatorOrgMSP"

	ComplaintOpen = "OPEN"

	HoldActive    = "ACTIVE"
	HoldReleased  = "RELEASED"
	HoldDisbursed = "DISBURSED"

	OrderPending  = "PENDING_REPAYMENT"
	OrderComplete = "COMPLETE"

	EventHoldPlaced   = "HoldPlaced"
	EventHoldReleased = "HoldReleased"
	EventDisbursal    = "DisbursalOrdered"
	EventRepaid       = "RepaymentConfirmed"

	prefixBank      = "bank:"
	prefixComplaint = "complaint:"
	prefixOrigin    = "origin:"
	prefixTransfer  = "transfer:"
	prefixHold      = "hold:"
	prefixAccount   = "account:"
	prefixOrder     = "order:"
	keyProvisions   = "provisions"

	maxTraceDepth = 25
)

// peerMSPID reports which org's peer this chaincode instance is running on.
var peerMSPID = shim.GetMSPID

// defaultProvisions are the legal bases a hold may cite until the Investigator
// replaces the list.
func defaultProvisions() []string {
	return []string{"BNSS-106", "BNSS-107", "PMLA-5", "PMLA-17", "COURT-ORDER"}
}

type Bank struct {
	DocType    string `json:"docType"`
	BankID     string `json:"bankId"`
	MSPID      string `json:"mspId"`
	Collection string `json:"collection"` // private collection shared with the Investigator
}

type Complaint struct {
	DocType           string   `json:"docType"`
	ComplaintID       string   `json:"complaintId"`
	VictimBank        string   `json:"victimBank"`
	VictimAccountHash string   `json:"victimAccountHash"`
	AmountLost        int64    `json:"amountLost"` // paise
	OriginRRNs        []string `json:"originRrns"` // the victim's own payments to the fraudster
	CaseRef           string   `json:"caseRef"`
	Status            string   `json:"status"`
	FiledAt           int64    `json:"filedAt"`
	Held              int64    `json:"held"`      // currently held for this complaint, across banks
	Recovered         int64    `json:"recovered"` // repaid to the victim so far
	// OverlapsWith lists other complaints that cite one of the same payments.
	OverlapsWith []string `json:"overlapsWith"`
	TxID         string   `json:"txId"`
}

// Transfer is one movement of money, recorded by the bank that received it.
type Transfer struct {
	DocType         string `json:"docType"`
	RRN             string `json:"rrn"`
	ParentRRN       string `json:"parentRrn"` // the traced transfer this money came from; empty for a victim's payment
	FromBank        string `json:"fromBank"`
	FromAccountHash string `json:"fromAccountHash"`
	ToBank          string `json:"toBank"`
	ToAccountHash   string `json:"toAccountHash"`
	Amount          int64  `json:"amount"`
	// Tainted is how much of Amount is victims' money. MovedOn is how much of
	// that has since left for another account, and Held how much is under hold.
	Tainted    int64    `json:"tainted"`
	MovedOn    int64    `json:"movedOn"`
	Held       int64    `json:"held"`
	Children   []string `json:"children"`
	HoldIDs    []string `json:"holdIds"`
	RecordedAt int64    `json:"recordedAt"`
	TxID       string   `json:"txId"`
}

func (t *Transfer) available() int64 { return t.Tainted - t.MovedOn - t.Held }

type Hold struct {
	DocType        string `json:"docType"`
	HoldID         string `json:"holdId"`
	ComplaintID    string `json:"complaintId"`
	RRN            string `json:"rrn"`
	BankID         string `json:"bankId"`
	AccountHash    string `json:"accountHash"`
	Amount         int64  `json:"amount"`
	LegalProvision string `json:"legalProvision"`
	CaseRef        string `json:"caseRef"`
	Status         string `json:"status"`
	PlacedBy       string `json:"placedBy"`
	PlacedAt       int64  `json:"placedAt"`
	ClosedAt       int64  `json:"closedAt"`
	ClosedReason   string `json:"closedReason"`
	OrderID        string `json:"orderId"`
	TxID           string `json:"txId"`
}

type accountIndex struct {
	HoldIDs []string `json:"holdIds"`
}

type Share struct {
	ComplaintID string `json:"complaintId"`
	VictimBank  string `json:"victimBank"`
	AmountLost  int64  `json:"amountLost"`
	HeldHere    int64  `json:"heldHere"` // held for this complaint in this account
	Amount      int64  `json:"amount"`   // this victim's share of what was recovered
	Repaid      bool   `json:"repaid"`
	RepaidAt    int64  `json:"repaidAt"`
}

// Order is a court-ordered recovery from one account and how it was shared out.
type Order struct {
	DocType     string `json:"docType"`
	OrderID     string `json:"orderId"`
	BankID      string `json:"bankId"`
	AccountHash string `json:"accountHash"`
	OrderRef    string `json:"orderRef"`
	TotalHeld   int64  `json:"totalHeld"`
	Recovered   int64  `json:"recovered"`
	// ReleasedToAccount is what was held but not recovered; it is freed.
	ReleasedToAccount int64   `json:"releasedToAccount"`
	Shares            []Share `json:"shares"`
	Status            string  `json:"status"`
	OrderedAt         int64   `json:"orderedAt"`
	CompletedAt       int64   `json:"completedAt"`
	TxID              string  `json:"txId"`
}

type TraceRow struct {
	Depth           int      `json:"depth"`
	RRN             string   `json:"rrn"`
	ParentRRN       string   `json:"parentRrn"`
	FromBank        string   `json:"fromBank"`
	ToBank          string   `json:"toBank"`
	ToAccountHash   string   `json:"toAccountHash"`
	Amount          int64    `json:"amount"`
	Tainted         int64    `json:"tainted"`
	MovedOn         int64    `json:"movedOn"`
	Held            int64    `json:"held"`
	AvailableToHold int64    `json:"availableToHold"`
	HoldIDs         []string `json:"holdIds"`
}

// ---- pure rules -------------------------------------------------------------

type claim struct {
	weight int64 // what the victim lost
	cap    int64 // what is held for that victim in this account
}

// prorata splits total among claims in proportion to weight, never giving a
// claim more than its cap. Money a capped claim cannot take is shared among
// the rest by the same rule. Any paise left by rounding go one each to the
// claims with the largest remainders, so the shares add up exactly.
func prorata(total int64, claims []claim) []int64 {
	out := make([]int64, len(claims))
	fixed := make([]bool, len(claims))
	remaining := total
	for {
		var sumW int64
		for i, c := range claims {
			if !fixed[i] {
				sumW += c.weight
			}
		}
		if sumW == 0 || remaining <= 0 {
			return out
		}
		capped := false
		for i, c := range claims {
			if fixed[i] {
				continue
			}
			share := mulDiv(remaining, c.weight, sumW)
			if share >= c.cap {
				out[i], fixed[i], capped = c.cap, true, true
			}
		}
		if capped {
			remaining = total
			for i := range claims {
				if fixed[i] {
					remaining -= out[i]
				}
			}
			continue
		}
		type rem struct {
			i    int
			frac *big.Int
		}
		var rems []rem
		var given int64
		for i, c := range claims {
			if fixed[i] {
				continue
			}
			out[i] = mulDiv(remaining, c.weight, sumW)
			given += out[i]
			f := new(big.Int).Mul(big.NewInt(remaining), big.NewInt(c.weight))
			f.Mod(f, big.NewInt(sumW))
			rems = append(rems, rem{i, f})
		}
		sort.SliceStable(rems, func(a, b int) bool { return rems[a].frac.Cmp(rems[b].frac) > 0 })
		left := remaining - given
		for k := 0; left > 0 && k < len(rems); k++ {
			if out[rems[k].i] < claims[rems[k].i].cap {
				out[rems[k].i]++
				left--
			}
		}
		return out
	}
}

func mulDiv(a, b, c int64) int64 {
	x := new(big.Int).Mul(big.NewInt(a), big.NewInt(b))
	return x.Div(x, big.NewInt(c)).Int64()
}

func validID(id string, minLen int) bool {
	if len(id) < minLen || len(id) > 40 {
		return false
	}
	for _, r := range id {
		if !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '_' && r != '-' {
			return false
		}
	}
	return true
}

func validHash(h string) bool {
	if len(h) != 64 {
		return false
	}
	for _, r := range h {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func rupees(paise int64) string {
	return fmt.Sprintf("Rs %d.%02d", paise/100, paise%100)
}

// ---- helpers ----------------------------------------------------------------

func callerMSP(ctx contractapi.TransactionContextInterface) (string, error) {
	msp, err := ctx.GetClientIdentity().GetMSPID()
	if err != nil {
		return "", fmt.Errorf("cannot read caller identity: %v", err)
	}
	return msp, nil
}

// endorsedBy makes the transaction fail on any peer outside the listed orgs.
// With the chaincode policy (Investigator AND one bank) this pins the
// endorsers to the Investigator and the one bank the action concerns.
func endorsedBy(action string, orgs ...string) error {
	here, err := peerMSPID()
	if err != nil {
		return fmt.Errorf("cannot tell which org's peer this is: %v", err)
	}
	for _, o := range orgs {
		if here == o {
			return nil
		}
	}
	return fmt.Errorf("%s must be endorsed by %s; %s's peer is not party to it", action, strings.Join(orgs, " and "), here)
}

func txTime(ctx contractapi.TransactionContextInterface) (int64, error) {
	ts, err := ctx.GetStub().GetTxTimestamp()
	if err != nil {
		return 0, fmt.Errorf("cannot read transaction timestamp: %v", err)
	}
	return ts.GetSeconds(), nil
}

func getJSON(ctx contractapi.TransactionContextInterface, key string, out interface{}) (bool, error) {
	raw, err := ctx.GetStub().GetState(key)
	if err != nil {
		return false, fmt.Errorf("failed to read %s: %v", key, err)
	}
	if raw == nil {
		return false, nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return false, fmt.Errorf("corrupt record at %s: %v", key, err)
	}
	return true, nil
}

func putJSON(ctx contractapi.TransactionContextInterface, key string, v interface{}) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return ctx.GetStub().PutState(key, raw)
}

func emit(ctx contractapi.TransactionContextInterface, name string, v interface{}) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return ctx.GetStub().SetEvent(name, raw)
}

func load[T any](ctx contractapi.TransactionContextInterface, key, what string) (*T, error) {
	var v T
	ok, err := getJSON(ctx, key, &v)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("%s does not exist", what)
	}
	return &v, nil
}

func loadBank(ctx contractapi.TransactionContextInterface, bankID string) (*Bank, error) {
	return load[Bank](ctx, prefixBank+bankID, "bank "+bankID)
}

func accountKey(bankID, accountHash string) string {
	return prefixAccount + bankID + ":" + accountHash
}

// putDetails stores the transient "details" value, if any, in the private
// collection the given bank shares with the Investigator.
func putDetails(ctx contractapi.TransactionContextInterface, bank *Bank, key string, required bool) error {
	tm, err := ctx.GetStub().GetTransient()
	if err != nil {
		return fmt.Errorf("cannot read transient data: %v", err)
	}
	raw, ok := tm["details"]
	if !ok || len(raw) == 0 {
		if required {
			return fmt.Errorf(`case details must be sent as transient data under "details"`)
		}
		return nil
	}
	if !json.Valid(raw) {
		return fmt.Errorf("details must be valid JSON")
	}
	return ctx.GetStub().PutPrivateData(bank.Collection, key, raw)
}

func loadProvisions(ctx contractapi.TransactionContextInterface) ([]string, error) {
	list := defaultProvisions()
	_, err := getJSON(ctx, keyProvisions, &list)
	return list, err
}

// ---- setup ------------------------------------------------------------------

// RegisterBank records which org speaks for a bank and which private
// collection it shares with the Investigator. Investigator only.
func (c *FraudHoldContract) RegisterBank(ctx contractapi.TransactionContextInterface, bankID string, mspID string, collection string) error {
	msp, err := callerMSP(ctx)
	if err != nil {
		return err
	}
	if msp != InvestigatorMSP {
		return fmt.Errorf("only the investigating authority (%s) may register banks, caller is %s", InvestigatorMSP, msp)
	}
	if !validID(bankID, 2) || mspID == "" || collection == "" {
		return fmt.Errorf("bankId, mspId and collection are required")
	}
	if err := endorsedBy("registering "+bankID, InvestigatorMSP, mspID); err != nil {
		return err
	}
	var existing Bank
	if ok, err := getJSON(ctx, prefixBank+bankID, &existing); err != nil {
		return err
	} else if ok {
		return fmt.Errorf("bank %s is already registered", bankID)
	}
	return putJSON(ctx, prefixBank+bankID, Bank{DocType: "bank", BankID: bankID, MSPID: mspID, Collection: collection})
}

// SetProvisions replaces the list of legal provisions a hold may cite.
// Investigator only. listJSON is a JSON array of strings.
func (c *FraudHoldContract) SetProvisions(ctx contractapi.TransactionContextInterface, listJSON string) error {
	msp, err := callerMSP(ctx)
	if err != nil {
		return err
	}
	if msp != InvestigatorMSP {
		return fmt.Errorf("only the investigating authority (%s) may set the accepted legal provisions, caller is %s", InvestigatorMSP, msp)
	}
	var list []string
	if err := json.Unmarshal([]byte(listJSON), &list); err != nil || len(list) == 0 {
		return fmt.Errorf("provisions must be a non-empty JSON array of strings")
	}
	for _, p := range list {
		if !validID(p, 3) {
			return fmt.Errorf("provision %q must be 3-40 characters of A-Z, 0-9, _ or -", p)
		}
	}
	return putJSON(ctx, keyProvisions, list)
}

func (c *FraudHoldContract) GetProvisions(ctx contractapi.TransactionContextInterface) ([]string, error) {
	return loadProvisions(ctx)
}

// ---- complaints and tracing -------------------------------------------------

// FileComplaint is called by the victim's bank. originRrnsJSON lists the
// victim's own payments to the fraudster. The victim's name, contact and
// account of events travel as transient "details" and are stored privately.
func (c *FraudHoldContract) FileComplaint(ctx contractapi.TransactionContextInterface, complaintID string, victimBank string, victimAccountHash string, amountLost int64, originRrnsJSON string, caseRef string) (*Complaint, error) {
	msp, err := callerMSP(ctx)
	if err != nil {
		return nil, err
	}
	bank, err := loadBank(ctx, victimBank)
	if err != nil {
		return nil, err
	}
	if msp != bank.MSPID {
		return nil, fmt.Errorf("a complaint is filed by the victim's own bank (%s), caller is %s", bank.MSPID, msp)
	}
	if err := endorsedBy("a complaint at "+victimBank, InvestigatorMSP, bank.MSPID); err != nil {
		return nil, err
	}
	if !validID(complaintID, 4) {
		return nil, fmt.Errorf("complaintId must be 4-40 characters of A-Z, 0-9, _ or -")
	}
	if !validID(caseRef, 4) {
		return nil, fmt.Errorf("a case reference is required (4-40 characters of A-Z, 0-9, _ or -)")
	}
	if !validHash(victimAccountHash) {
		return nil, fmt.Errorf("victimAccountHash must be a SHA-256 hex digest; account numbers are never stored")
	}
	if amountLost <= 0 {
		return nil, fmt.Errorf("amountLost must be positive")
	}
	var rrns []string
	if err := json.Unmarshal([]byte(originRrnsJSON), &rrns); err != nil || len(rrns) == 0 || len(rrns) > 20 {
		return nil, fmt.Errorf("originRrns must be a JSON array of 1 to 20 payment references")
	}
	seen := map[string]bool{}
	for _, r := range rrns {
		if !validID(r, 6) || seen[r] {
			return nil, fmt.Errorf("origin RRN %q is invalid or repeated", r)
		}
		seen[r] = true
	}
	var existing Complaint
	if ok, err := getJSON(ctx, prefixComplaint+complaintID, &existing); err != nil {
		return nil, err
	} else if ok {
		return nil, fmt.Errorf("complaint %s is already filed", complaintID)
	}
	now, err := txTime(ctx)
	if err != nil {
		return nil, err
	}
	comp := Complaint{DocType: "complaint", ComplaintID: complaintID, VictimBank: victimBank,
		VictimAccountHash: victimAccountHash, AmountLost: amountLost, OriginRRNs: rrns, CaseRef: caseRef,
		Status: ComplaintOpen, FiledAt: now, OverlapsWith: []string{}, TxID: ctx.GetStub().GetTxID()}
	for _, r := range rrns {
		var citing []string
		if _, err := getJSON(ctx, prefixOrigin+r, &citing); err != nil {
			return nil, err
		}
		for _, other := range citing {
			if !contains(comp.OverlapsWith, other) {
				comp.OverlapsWith = append(comp.OverlapsWith, other)
			}
		}
		if err := putJSON(ctx, prefixOrigin+r, append(citing, complaintID)); err != nil {
			return nil, err
		}
	}
	if err := putJSON(ctx, prefixComplaint+complaintID, comp); err != nil {
		return nil, err
	}
	if err := putDetails(ctx, bank, "complaint:"+complaintID, true); err != nil {
		return nil, err
	}
	return &comp, nil
}

// RecordTransfer is called by the bank that received the money. With an empty
// parentRrn it records a victim's own payment, which some complaint must
// already cite. Otherwise it records money moving on from a traced transfer,
// and carries forward as much of the victims' money as was still sitting there.
func (c *FraudHoldContract) RecordTransfer(ctx contractapi.TransactionContextInterface, rrn string, parentRrn string, fromBank string, fromAccountHash string, toBank string, toAccountHash string, amount int64) (*Transfer, error) {
	msp, err := callerMSP(ctx)
	if err != nil {
		return nil, err
	}
	to, err := loadBank(ctx, toBank)
	if err != nil {
		return nil, err
	}
	if msp != to.MSPID {
		return nil, fmt.Errorf("a transfer is recorded by the bank that received it (%s), caller is %s", to.MSPID, msp)
	}
	if err := endorsedBy("a transfer into "+toBank, InvestigatorMSP, to.MSPID); err != nil {
		return nil, err
	}
	if _, err := loadBank(ctx, fromBank); err != nil {
		return nil, err
	}
	if !validID(rrn, 6) || !validHash(fromAccountHash) || !validHash(toAccountHash) || amount <= 0 {
		return nil, fmt.Errorf("rrn, both account hashes and a positive amount are required")
	}
	var existing Transfer
	if ok, err := getJSON(ctx, prefixTransfer+rrn, &existing); err != nil {
		return nil, err
	} else if ok {
		return nil, fmt.Errorf("transfer %s is already recorded", rrn)
	}
	now, err := txTime(ctx)
	if err != nil {
		return nil, err
	}
	tr := Transfer{DocType: "transfer", RRN: rrn, ParentRRN: parentRrn, FromBank: fromBank, FromAccountHash: fromAccountHash,
		ToBank: toBank, ToAccountHash: toAccountHash, Amount: amount, Children: []string{}, HoldIDs: []string{},
		RecordedAt: now, TxID: ctx.GetStub().GetTxID()}

	if parentRrn == "" {
		var citing []string
		if ok, err := getJSON(ctx, prefixOrigin+rrn, &citing); err != nil {
			return nil, err
		} else if !ok || len(citing) == 0 {
			return nil, fmt.Errorf("no complaint cites payment %s; only a victim's payment can start a trace", rrn)
		}
		matches := false
		for _, id := range citing {
			comp, err := load[Complaint](ctx, prefixComplaint+id, "complaint "+id)
			if err != nil {
				return nil, err
			}
			if comp.VictimBank == fromBank && comp.VictimAccountHash == fromAccountHash {
				matches = true
			}
		}
		if !matches {
			return nil, fmt.Errorf("payment %s did not come from the account of a victim who cites it", rrn)
		}
		tr.Tainted = amount
	} else {
		parent, err := load[Transfer](ctx, prefixTransfer+parentRrn, "parent transfer "+parentRrn)
		if err != nil {
			return nil, err
		}
		if parent.ToBank != fromBank || parent.ToAccountHash != fromAccountHash {
			return nil, fmt.Errorf("transfer %s does not leave the account that %s arrived in", rrn, parentRrn)
		}
		// Only money that is still there, and not already held, can have moved on.
		tainted := parent.available()
		if tainted <= 0 {
			return nil, fmt.Errorf("none of the victims' money from %s was left in that account to move on", parentRrn)
		}
		if tainted > amount {
			tainted = amount
		}
		tr.Tainted = tainted
		parent.MovedOn += tainted
		parent.Children = append(parent.Children, rrn)
		if err := putJSON(ctx, prefixTransfer+parentRrn, parent); err != nil {
			return nil, err
		}
	}
	if err := putJSON(ctx, prefixTransfer+rrn, tr); err != nil {
		return nil, err
	}
	return &tr, nil
}

// tracedBy reports whether a transfer descends from one of the complaint's payments.
func tracedBy(ctx contractapi.TransactionContextInterface, comp *Complaint, tr *Transfer) (bool, error) {
	cur := tr
	for depth := 0; depth < maxTraceDepth; depth++ {
		if cur.ParentRRN == "" {
			return contains(comp.OriginRRNs, cur.RRN), nil
		}
		parent, err := load[Transfer](ctx, prefixTransfer+cur.ParentRRN, "transfer "+cur.ParentRRN)
		if err != nil {
			return false, err
		}
		cur = parent
	}
	return false, fmt.Errorf("trace for %s is deeper than %d hops", tr.RRN, maxTraceDepth)
}

// ---- holds ------------------------------------------------------------------

// PlaceHold holds part of one traced transfer where it now sits. It is refused
// without a legal provision and the complaint's case reference, refused beyond
// what is still traceable to that transfer, and refused as a duplicate when
// those funds are already held. Either the Investigator or the holding bank
// may submit it; both must endorse it.
func (c *FraudHoldContract) PlaceHold(ctx contractapi.TransactionContextInterface, holdID string, complaintID string, rrn string, amount int64, legalProvision string, caseRef string) (*Hold, error) {
	msp, err := callerMSP(ctx)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(legalProvision) == "" {
		return nil, fmt.Errorf("hold refused: no legal provision cited")
	}
	if strings.TrimSpace(caseRef) == "" {
		return nil, fmt.Errorf("hold refused: no case reference given")
	}
	provisions, err := loadProvisions(ctx)
	if err != nil {
		return nil, err
	}
	if !contains(provisions, legalProvision) {
		return nil, fmt.Errorf("hold refused: %q is not an accepted legal provision (accepted: %s)", legalProvision, strings.Join(provisions, ", "))
	}
	if !validID(holdID, 4) {
		return nil, fmt.Errorf("holdId must be 4-40 characters of A-Z, 0-9, _ or -")
	}
	if amount <= 0 {
		return nil, fmt.Errorf("hold amount must be positive")
	}
	comp, err := load[Complaint](ctx, prefixComplaint+complaintID, "complaint "+complaintID)
	if err != nil {
		return nil, err
	}
	if caseRef != comp.CaseRef {
		return nil, fmt.Errorf("hold refused: case reference %s does not match complaint %s", caseRef, complaintID)
	}
	tr, err := load[Transfer](ctx, prefixTransfer+rrn, "traced transfer "+rrn)
	if err != nil {
		return nil, err
	}
	bank, err := loadBank(ctx, tr.ToBank)
	if err != nil {
		return nil, err
	}
	if msp != InvestigatorMSP && msp != bank.MSPID {
		return nil, fmt.Errorf("a hold at %s can be requested by the investigating authority or %s, caller is %s", tr.ToBank, bank.MSPID, msp)
	}
	if err := endorsedBy("a hold at "+tr.ToBank, InvestigatorMSP, bank.MSPID); err != nil {
		return nil, err
	}
	var dup Hold
	if ok, err := getJSON(ctx, prefixHold+holdID, &dup); err != nil {
		return nil, err
	} else if ok {
		return nil, fmt.Errorf("hold %s already exists", holdID)
	}
	if ok, err := tracedBy(ctx, comp, tr); err != nil {
		return nil, err
	} else if !ok {
		return nil, fmt.Errorf("hold refused: transfer %s is not traced from complaint %s", rrn, complaintID)
	}
	if avail := tr.available(); amount > avail {
		if tr.Held > 0 {
			return nil, fmt.Errorf("duplicate hold refused: %s of the funds from %s are already held under %s, leaving %s to hold",
				rupees(tr.Held), rrn, strings.Join(tr.HoldIDs, ", "), rupees(avail))
		}
		return nil, fmt.Errorf("hold refused: only %s of the victims' money from %s is still in that account, not %s", rupees(avail), rrn, rupees(amount))
	}
	if outstanding := comp.AmountLost - comp.Recovered - comp.Held; amount > outstanding {
		return nil, fmt.Errorf("hold refused: complaint %s has %s left to recover; a hold cannot exceed the disputed amount", complaintID, rupees(outstanding))
	}
	now, err := txTime(ctx)
	if err != nil {
		return nil, err
	}
	hold := Hold{DocType: "hold", HoldID: holdID, ComplaintID: complaintID, RRN: rrn, BankID: tr.ToBank,
		AccountHash: tr.ToAccountHash, Amount: amount, LegalProvision: legalProvision, CaseRef: caseRef,
		Status: HoldActive, PlacedBy: msp, PlacedAt: now, TxID: ctx.GetStub().GetTxID()}
	tr.Held += amount
	tr.HoldIDs = append(tr.HoldIDs, holdID)
	comp.Held += amount
	var idx accountIndex
	if _, err := getJSON(ctx, accountKey(hold.BankID, hold.AccountHash), &idx); err != nil {
		return nil, err
	}
	idx.HoldIDs = append(idx.HoldIDs, holdID)
	for key, v := range map[string]interface{}{
		prefixHold + holdID: hold, prefixTransfer + rrn: tr, prefixComplaint + complaintID: comp,
		accountKey(hold.BankID, hold.AccountHash): idx} {
		if err := putJSON(ctx, key, v); err != nil {
			return nil, err
		}
	}
	if err := putDetails(ctx, bank, "hold:"+holdID, false); err != nil {
		return nil, err
	}
	if err := emit(ctx, EventHoldPlaced, hold); err != nil {
		return nil, err
	}
	return &hold, nil
}

// ReleaseHold lifts a hold without recovering anything, for example when a
// complaint is withdrawn. Investigator only, endorsed with the holding bank.
func (c *FraudHoldContract) ReleaseHold(ctx contractapi.TransactionContextInterface, holdID string, reason string) (*Hold, error) {
	msp, err := callerMSP(ctx)
	if err != nil {
		return nil, err
	}
	if msp != InvestigatorMSP {
		return nil, fmt.Errorf("only the investigating authority (%s) may release a hold, caller is %s", InvestigatorMSP, msp)
	}
	hold, err := load[Hold](ctx, prefixHold+holdID, "hold "+holdID)
	if err != nil {
		return nil, err
	}
	bank, err := loadBank(ctx, hold.BankID)
	if err != nil {
		return nil, err
	}
	if err := endorsedBy("releasing a hold at "+hold.BankID, InvestigatorMSP, bank.MSPID); err != nil {
		return nil, err
	}
	if hold.Status != HoldActive {
		return nil, fmt.Errorf("hold %s is %s, not active", holdID, hold.Status)
	}
	if strings.TrimSpace(reason) == "" {
		return nil, fmt.Errorf("a reason is required to release a hold")
	}
	tr, err := load[Transfer](ctx, prefixTransfer+hold.RRN, "transfer "+hold.RRN)
	if err != nil {
		return nil, err
	}
	comp, err := load[Complaint](ctx, prefixComplaint+hold.ComplaintID, "complaint "+hold.ComplaintID)
	if err != nil {
		return nil, err
	}
	now, err := txTime(ctx)
	if err != nil {
		return nil, err
	}
	tr.Held -= hold.Amount
	comp.Held -= hold.Amount
	hold.Status, hold.ClosedAt, hold.ClosedReason = HoldReleased, now, reason
	if err := putJSON(ctx, prefixTransfer+hold.RRN, tr); err != nil {
		return nil, err
	}
	if err := putJSON(ctx, prefixComplaint+hold.ComplaintID, comp); err != nil {
		return nil, err
	}
	if err := putJSON(ctx, prefixHold+holdID, hold); err != nil {
		return nil, err
	}
	if err := emit(ctx, EventHoldReleased, hold); err != nil {
		return nil, err
	}
	return hold, nil
}

// ---- recovery ---------------------------------------------------------------

// OrderDisbursal records a court order to recover money from one account. The
// amount recovered is shared among the victims with holds on that account in
// proportion to what each lost, never more than was held for them. Whatever
// was held but not recovered is freed. Investigator only, endorsed with the
// holding bank.
func (c *FraudHoldContract) OrderDisbursal(ctx contractapi.TransactionContextInterface, orderID string, bankID string, accountHash string, recoveredAmount int64, orderRef string) (*Order, error) {
	msp, err := callerMSP(ctx)
	if err != nil {
		return nil, err
	}
	if msp != InvestigatorMSP {
		return nil, fmt.Errorf("only the investigating authority (%s) may record a disbursal order, caller is %s", InvestigatorMSP, msp)
	}
	bank, err := loadBank(ctx, bankID)
	if err != nil {
		return nil, err
	}
	if err := endorsedBy("a disbursal from "+bankID, InvestigatorMSP, bank.MSPID); err != nil {
		return nil, err
	}
	if !validID(orderID, 4) || !validID(orderRef, 4) {
		return nil, fmt.Errorf("orderId and the court order reference are required (4-40 characters of A-Z, 0-9, _ or -)")
	}
	var existing Order
	if ok, err := getJSON(ctx, prefixOrder+orderID, &existing); err != nil {
		return nil, err
	} else if ok {
		return nil, fmt.Errorf("duplicate: order %s is already recorded", orderID)
	}
	var idx accountIndex
	if _, err := getJSON(ctx, accountKey(bankID, accountHash), &idx); err != nil {
		return nil, err
	}
	now, err := txTime(ctx)
	if err != nil {
		return nil, err
	}

	heldFor := map[string]int64{}
	var active []*Hold
	var total int64
	for _, id := range idx.HoldIDs {
		h, err := load[Hold](ctx, prefixHold+id, "hold "+id)
		if err != nil {
			return nil, err
		}
		if h.Status != HoldActive {
			continue
		}
		active = append(active, h)
		heldFor[h.ComplaintID] += h.Amount
		total += h.Amount
	}
	if len(active) == 0 {
		return nil, fmt.Errorf("there is no active hold on that account at %s", bankID)
	}
	if recoveredAmount <= 0 || recoveredAmount > total {
		return nil, fmt.Errorf("recovered amount must be between Rs 0.01 and the %s held on that account", rupees(total))
	}
	ids := make([]string, 0, len(heldFor))
	for id := range heldFor {
		ids = append(ids, id)
	}
	sort.Strings(ids) // fixed order: every peer must compute the same split
	comps := make([]*Complaint, len(ids))
	claims := make([]claim, len(ids))
	for i, id := range ids {
		comp, err := load[Complaint](ctx, prefixComplaint+id, "complaint "+id)
		if err != nil {
			return nil, err
		}
		comps[i] = comp
		claims[i] = claim{weight: comp.AmountLost, cap: heldFor[id]}
	}
	amounts := prorata(recoveredAmount, claims)

	order := Order{DocType: "order", OrderID: orderID, BankID: bankID, AccountHash: accountHash, OrderRef: orderRef,
		TotalHeld: total, Recovered: recoveredAmount, ReleasedToAccount: total - recoveredAmount,
		Shares: []Share{}, Status: OrderPending, OrderedAt: now, TxID: ctx.GetStub().GetTxID()}
	var sum int64
	for i, comp := range comps {
		order.Shares = append(order.Shares, Share{ComplaintID: comp.ComplaintID, VictimBank: comp.VictimBank,
			AmountLost: comp.AmountLost, HeldHere: heldFor[comp.ComplaintID], Amount: amounts[i]})
		sum += amounts[i]
		comp.Held -= heldFor[comp.ComplaintID]
		if err := putJSON(ctx, prefixComplaint+comp.ComplaintID, comp); err != nil {
			return nil, err
		}
	}
	if sum != recoveredAmount {
		return nil, fmt.Errorf("shares of %s do not add up to the %s recovered", rupees(sum), rupees(recoveredAmount))
	}
	// Two holds can sit on the same transfer. A read does not see this
	// transaction's own writes, so each transfer is loaded once and written once.
	transfers := map[string]*Transfer{}
	var touched []string
	for _, h := range active {
		tr := transfers[h.RRN]
		if tr == nil {
			tr, err = load[Transfer](ctx, prefixTransfer+h.RRN, "transfer "+h.RRN)
			if err != nil {
				return nil, err
			}
			transfers[h.RRN] = tr
			touched = append(touched, h.RRN)
		}
		// The held money has left the account, recovered or freed: nothing of it remains to hold.
		tr.Held -= h.Amount
		tr.MovedOn += h.Amount
		h.Status, h.ClosedAt, h.ClosedReason, h.OrderID = HoldDisbursed, now, "court order "+orderRef, orderID
		if err := putJSON(ctx, prefixHold+h.HoldID, h); err != nil {
			return nil, err
		}
	}
	for _, rrn := range touched {
		if err := putJSON(ctx, prefixTransfer+rrn, transfers[rrn]); err != nil {
			return nil, err
		}
	}
	if err := putJSON(ctx, prefixOrder+orderID, order); err != nil {
		return nil, err
	}
	if err := emit(ctx, EventDisbursal, order); err != nil {
		return nil, err
	}
	return &order, nil
}

// ConfirmRepayment is called by a victim's bank once it has credited the
// victim their share. When every share is confirmed the order is complete.
func (c *FraudHoldContract) ConfirmRepayment(ctx contractapi.TransactionContextInterface, orderID string, complaintID string) (*Order, error) {
	msp, err := callerMSP(ctx)
	if err != nil {
		return nil, err
	}
	order, err := load[Order](ctx, prefixOrder+orderID, "order "+orderID)
	if err != nil {
		return nil, err
	}
	holding, err := loadBank(ctx, order.BankID)
	if err != nil {
		return nil, err
	}
	if err := endorsedBy("a repayment out of "+order.BankID, InvestigatorMSP, holding.MSPID); err != nil {
		return nil, err
	}
	comp, err := load[Complaint](ctx, prefixComplaint+complaintID, "complaint "+complaintID)
	if err != nil {
		return nil, err
	}
	victimBank, err := loadBank(ctx, comp.VictimBank)
	if err != nil {
		return nil, err
	}
	if msp != victimBank.MSPID {
		return nil, fmt.Errorf("only the victim's bank (%s) can confirm repayment for %s, caller is %s", victimBank.MSPID, complaintID, msp)
	}
	now, err := txTime(ctx)
	if err != nil {
		return nil, err
	}
	found, allRepaid := false, true
	for i := range order.Shares {
		s := &order.Shares[i]
		if s.ComplaintID == complaintID {
			if s.Repaid {
				return nil, fmt.Errorf("duplicate: repayment for %s under order %s is already confirmed", complaintID, orderID)
			}
			s.Repaid, s.RepaidAt, found = true, now, true
			comp.Recovered += s.Amount
		}
		if !s.Repaid {
			allRepaid = false
		}
	}
	if !found {
		return nil, fmt.Errorf("complaint %s has no share in order %s", complaintID, orderID)
	}
	if allRepaid {
		order.Status, order.CompletedAt = OrderComplete, now
	}
	if err := putJSON(ctx, prefixComplaint+complaintID, comp); err != nil {
		return nil, err
	}
	if err := putJSON(ctx, prefixOrder+orderID, order); err != nil {
		return nil, err
	}
	if err := emit(ctx, EventRepaid, map[string]interface{}{"orderId": orderID, "complaintId": complaintID, "status": order.Status}); err != nil {
		return nil, err
	}
	return order, nil
}

// ---- queries ----------------------------------------------------------------

func (c *FraudHoldContract) GetComplaint(ctx contractapi.TransactionContextInterface, complaintID string) (*Complaint, error) {
	return load[Complaint](ctx, prefixComplaint+complaintID, "complaint "+complaintID)
}

func (c *FraudHoldContract) GetHold(ctx contractapi.TransactionContextInterface, holdID string) (*Hold, error) {
	return load[Hold](ctx, prefixHold+holdID, "hold "+holdID)
}

func (c *FraudHoldContract) GetOrder(ctx contractapi.TransactionContextInterface, orderID string) (*Order, error) {
	return load[Order](ctx, prefixOrder+orderID, "order "+orderID)
}

// GetTrace follows a complaint's money from the victim's payments through
// every recorded transfer, and shows how much could still be held at each stop.
func (c *FraudHoldContract) GetTrace(ctx contractapi.TransactionContextInterface, complaintID string) ([]*TraceRow, error) {
	comp, err := load[Complaint](ctx, prefixComplaint+complaintID, "complaint "+complaintID)
	if err != nil {
		return nil, err
	}
	rows := []*TraceRow{}
	var walk func(rrn string, depth int) error
	walk = func(rrn string, depth int) error {
		if depth > maxTraceDepth {
			return fmt.Errorf("trace is deeper than %d hops", maxTraceDepth)
		}
		var tr Transfer
		if ok, err := getJSON(ctx, prefixTransfer+rrn, &tr); err != nil {
			return err
		} else if !ok {
			return nil // cited by the victim, not yet recorded by the receiving bank
		}
		rows = append(rows, &TraceRow{Depth: depth, RRN: tr.RRN, ParentRRN: tr.ParentRRN, FromBank: tr.FromBank,
			ToBank: tr.ToBank, ToAccountHash: tr.ToAccountHash, Amount: tr.Amount, Tainted: tr.Tainted,
			MovedOn: tr.MovedOn, Held: tr.Held, AvailableToHold: tr.available(), HoldIDs: tr.HoldIDs})
		for _, child := range tr.Children {
			if err := walk(child, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	for _, rrn := range comp.OriginRRNs {
		if err := walk(rrn, 1); err != nil {
			return nil, err
		}
	}
	return rows, nil
}

// GetAccountHolds lists every hold ever placed on one account.
func (c *FraudHoldContract) GetAccountHolds(ctx contractapi.TransactionContextInterface, bankID string, accountHash string) ([]*Hold, error) {
	var idx accountIndex
	if _, err := getJSON(ctx, accountKey(bankID, accountHash), &idx); err != nil {
		return nil, err
	}
	out := []*Hold{}
	for _, id := range idx.HoldIDs {
		h, err := load[Hold](ctx, prefixHold+id, "hold "+id)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, nil
}

// GetCaseDetails returns the private details stored with a complaint or a
// hold. kind is "complaint" or "hold". It fails for any org other than the
// Investigator and the bank concerned.
func (c *FraudHoldContract) GetCaseDetails(ctx contractapi.TransactionContextInterface, kind string, id string) (string, error) {
	var bankID string
	switch kind {
	case "complaint":
		comp, err := load[Complaint](ctx, prefixComplaint+id, "complaint "+id)
		if err != nil {
			return "", err
		}
		bankID = comp.VictimBank
	case "hold":
		h, err := load[Hold](ctx, prefixHold+id, "hold "+id)
		if err != nil {
			return "", err
		}
		bankID = h.BankID
	default:
		return "", fmt.Errorf(`kind must be "complaint" or "hold"`)
	}
	bank, err := loadBank(ctx, bankID)
	if err != nil {
		return "", err
	}
	raw, err := ctx.GetStub().GetPrivateData(bank.Collection, kind+":"+id)
	if err != nil {
		return "", fmt.Errorf("case details are not available to this org: %v", err)
	}
	if raw == nil {
		return "", fmt.Errorf("no case details are available to this org for %s %s", kind, id)
	}
	return string(raw), nil
}
