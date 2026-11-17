package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"

	"github.com/hyperledger/fabric-contract-api-go/v2/contractapi"
)

// SupplyChainContract lets an anchor buyer's single payment reach every tier of
// its supply chain in one commit, without moving any tier's money unless that
// tier asked for it.
//
// Each party registers once with its own identity. The buyer opens a chain with
// what it owes its Tier-1 supplier. Each supplier then registers what it owes
// its own downstream supplier, and the tier above it approves, since it is that
// tier's payment being split. When the buyer releases payment, one transaction
// pays every approved tier its net share.
//
// Amounts and invoice references are contract values: they live in a private
// data collection shared only by the orgs that take part. Everyone else on the
// channel sees that an obligation exists and its dates, not what it is worth.
//
// The chaincode endorsement policy is Buyer AND BankA AND BankB, the orgs the
// parties act through, so every obligation is endorsed by the org of the tier
// registering it and the org of the tier above.
type SupplyChainContract struct {
	contractapi.Contract
}

const (
	BuyerMSP   = "BuyerOrgMSP"
	Collection = "scTerms"

	RoleBuyer    = "BUYER"
	RoleSupplier = "SUPPLIER"

	ClassMicro  = "MICRO"
	ClassSmall  = "SMALL"
	ClassMedium = "MEDIUM"

	ObRegistered = "REGISTERED" // registered by the tier that owes it, awaiting the tier above
	ObActive     = "ACTIVE"     // approved: will be paid when the buyer releases
	ObPaid       = "PAID"
	ObWithdrawn  = "WITHDRAWN"
	ObExcluded   = "EXCLUDED" // never approved, so left out of the release

	ChainOpen     = "OPEN"
	ChainReleased = "RELEASED"

	EventReleased = "ChainReleased"

	FlagOK         = "OK"
	FlagDueSoon    = "DUE_SOON"
	FlagOverdue    = "OVERDUE"
	FlagNotCovered = "NOT_COVERED"

	prefixParty = "party:"
	prefixCert  = "cert:"
	prefixChain = "chain:"
	prefixOb    = "ob:"
	keyTerms    = "terms:"
	keyRelease  = "release:"

	day = 86400
)

var udyamPattern = regexp.MustCompile(`^UDYAM-[A-Z]{2}-[0-9]{2}-[0-9]{7}$`)

type Party struct {
	DocType      string `json:"docType"`
	PartyID      string `json:"partyId"`
	Name         string `json:"name"`
	Role         string `json:"role"`
	Udyam        string `json:"udyam"` // Udyam registration number; empty for a buyer
	Class        string `json:"class"` // MICRO, SMALL or MEDIUM; empty for a buyer
	BankID       string `json:"bankId"`
	AccountHash  string `json:"accountHash"`
	MSPID        string `json:"mspId"`
	CertHash     string `json:"certHash"` // the one identity that may act as this party
	RegisteredAt int64  `json:"registeredAt"`
}

type Chain struct {
	DocType         string   `json:"docType"`
	ChainID         string   `json:"chainId"`
	BuyerID         string   `json:"buyerId"`
	Status          string   `json:"status"`
	ObligationIDs   []string `json:"obligationIds"`
	PartyIDs        []string `json:"partyIds"`
	CreatedAt       int64    `json:"createdAt"`
	ReleasedAt      int64    `json:"releasedAt"`
	PaymentRef      string   `json:"paymentRef"`
	ReleaseTxID     string   `json:"releaseTxId"`
	CreditedParties []string `json:"creditedParties"`
}

// Obligation is the public part of what one party owes the next tier down.
type Obligation struct {
	DocType          string `json:"docType"`
	ObligationID     string `json:"obligationId"`
	ChainID          string `json:"chainId"`
	Level            int    `json:"level"` // 1 = buyer to Tier-1, 2 = Tier-1 to Tier-2, ...
	DebtorID         string `json:"debtorId"`
	CreditorID       string `json:"creditorId"`
	ApproverID       string `json:"approverId"` // the tier above the debtor; empty at level 1
	Status           string `json:"status"`
	AcceptanceDate   int64  `json:"acceptanceDate"`   // when the goods or services were accepted
	AgreedCreditDays int    `json:"agreedCreditDays"` // from a written agreement; 0 if none
	DueDate          int64  `json:"dueDate"`
	CreditorUdyam    string `json:"creditorUdyam"`
	CreditorClass    string `json:"creditorClass"`
	// Covered43Bh is true when the creditor is a registered micro or small
	// enterprise, so Section 43B(h) of the Income Tax Act applies to the debtor.
	Covered43Bh      bool   `json:"covered43Bh"`
	StatutoryDueDate int64  `json:"statutoryDueDate"`
	RegisteredAt     int64  `json:"registeredAt"`
	ApprovedAt       int64  `json:"approvedAt"`
	PaidAt           int64  `json:"paidAt"`
	PaidOnTime       bool   `json:"paidOnTime"`
	DaysLate         int64  `json:"daysLate"`
	TxID             string `json:"txId"`
}

// Terms is the private part of an obligation.
type Terms struct {
	ObligationID string `json:"obligationId"`
	Amount       int64  `json:"amount"` // paise
	InvoiceRef   string `json:"invoiceRef"`
}

type Credit struct {
	PartyID     string `json:"partyId"`
	Name        string `json:"name"`
	Level       int    `json:"level"`
	BankID      string `json:"bankId"`
	AccountHash string `json:"accountHash"`
	Received    int64  `json:"received"`   // owed to this party from the tier above
	PassedDown  int64  `json:"passedDown"` // of that, what it had registered for the tier below
	Net         int64  `json:"net"`        // what this party is actually credited
}

// Release is the private record of how one buyer payment was split.
type Release struct {
	ChainID    string   `json:"chainId"`
	PaymentRef string   `json:"paymentRef"`
	Total      int64    `json:"total"`
	Credits    []Credit `json:"credits"`
	ReleasedAt int64    `json:"releasedAt"`
	TxID       string   `json:"txId"`
}

type ChainView struct {
	Chain       Chain        `json:"chain"`
	Obligations []Obligation `json:"obligations"`
}

type DeadlineRow struct {
	ObligationID     string `json:"obligationId"`
	ChainID          string `json:"chainId"`
	Level            int    `json:"level"`
	DebtorID         string `json:"debtorId"`
	CreditorID       string `json:"creditorId"`
	CreditorUdyam    string `json:"creditorUdyam"`
	CreditorClass    string `json:"creditorClass"`
	Status           string `json:"status"`
	StatutoryDueDate int64  `json:"statutoryDueDate"`
	DaysRemaining    int64  `json:"daysRemaining"` // negative once overdue
	Flag             string `json:"flag"`
	// DeductionAtRisk: the debtor loses the tax deduction for this expense in
	// the year it was incurred unless it pays by the statutory date.
	DeductionAtRisk bool `json:"deductionAtRisk"`
	// Amount is -1 when the caller's org is not party to the private terms.
	Amount int64 `json:"amount"`
}

// ---- pure rules -------------------------------------------------------------

// statutoryDue applies Section 15 of the MSMED Act, which Section 43B(h)
// relies on: payment within the period agreed in writing, never more than 45
// days from acceptance, or within 15 days where nothing was agreed. It covers
// micro and small enterprises, not medium ones.
func statutoryDue(acceptance int64, agreedCreditDays int, class string) (covered bool, due int64) {
	if class != ClassMicro && class != ClassSmall {
		return false, 0
	}
	days := 15
	if agreedCreditDays > 0 {
		days = agreedCreditDays
		if days > 45 {
			days = 45
		}
	}
	return true, acceptance + int64(days)*day
}

// deadlineFlag classifies an unpaid obligation as of a moment in time.
func deadlineFlag(covered bool, due, asOf int64, warnDays int) (flag string, daysRemaining int64) {
	if !covered {
		return FlagNotCovered, 0
	}
	if asOf > due {
		return FlagOverdue, -((asOf - due + day - 1) / day)
	}
	daysRemaining = (due - asOf) / day
	if daysRemaining <= int64(warnDays) {
		return FlagDueSoon, daysRemaining
	}
	return FlagOK, daysRemaining
}

func validID(id string) bool {
	if len(id) < 2 || len(id) > 40 {
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

// ---- helpers ----------------------------------------------------------------

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

func getPrivate(ctx contractapi.TransactionContextInterface, key string, out interface{}) (bool, error) {
	raw, err := ctx.GetStub().GetPrivateData(Collection, key)
	if err != nil {
		return false, fmt.Errorf("failed to read private %s: %v", key, err)
	}
	if raw == nil {
		return false, nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return false, fmt.Errorf("corrupt private record at %s: %v", key, err)
	}
	return true, nil
}

func putPrivate(ctx contractapi.TransactionContextInterface, key string, v interface{}) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return ctx.GetStub().PutPrivateData(Collection, key, raw)
}

// transientTerms reads the amount and invoice reference from the transient
// field, so they never appear in the transaction that is ordered into a block.
func transientTerms(ctx contractapi.TransactionContextInterface) (int64, string, error) {
	tm, err := ctx.GetStub().GetTransient()
	if err != nil {
		return 0, "", fmt.Errorf("cannot read transient data: %v", err)
	}
	raw, ok := tm["terms"]
	if !ok {
		return 0, "", fmt.Errorf(`the amount and invoice reference must be sent as transient data under "terms"`)
	}
	var t struct {
		Amount     int64  `json:"amount"`
		InvoiceRef string `json:"invoiceRef"`
	}
	if err := json.Unmarshal(raw, &t); err != nil {
		return 0, "", fmt.Errorf("invalid terms: %v", err)
	}
	if t.Amount <= 0 {
		return 0, "", fmt.Errorf("amount must be positive")
	}
	if t.InvoiceRef == "" || len(t.InvoiceRef) > 64 {
		return 0, "", fmt.Errorf("invoiceRef is required (up to 64 characters)")
	}
	return t.Amount, t.InvoiceRef, nil
}

func certHash(ctx contractapi.TransactionContextInterface) (string, error) {
	id, err := ctx.GetClientIdentity().GetID()
	if err != nil {
		return "", fmt.Errorf("cannot read caller identity: %v", err)
	}
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:]), nil
}

// callerParty returns the party the calling identity registered as.
func callerParty(ctx contractapi.TransactionContextInterface) (*Party, error) {
	h, err := certHash(ctx)
	if err != nil {
		return nil, err
	}
	var partyID string
	if ok, err := getJSON(ctx, prefixCert+h, &partyID); err != nil {
		return nil, err
	} else if !ok {
		return nil, fmt.Errorf("this identity is not registered as a party")
	}
	return loadParty(ctx, partyID)
}

func loadParty(ctx contractapi.TransactionContextInterface, partyID string) (*Party, error) {
	var p Party
	ok, err := getJSON(ctx, prefixParty+partyID, &p)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("party %s is not registered", partyID)
	}
	return &p, nil
}

func loadChain(ctx contractapi.TransactionContextInterface, chainID string) (*Chain, error) {
	var c Chain
	ok, err := getJSON(ctx, prefixChain+chainID, &c)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("chain %s does not exist", chainID)
	}
	return &c, nil
}

func loadObligation(ctx contractapi.TransactionContextInterface, id string) (*Obligation, error) {
	var o Obligation
	ok, err := getJSON(ctx, prefixOb+id, &o)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("obligation %s does not exist", id)
	}
	return &o, nil
}

func loadTerms(ctx contractapi.TransactionContextInterface, obligationID string) (*Terms, error) {
	var t Terms
	ok, err := getPrivate(ctx, keyTerms+obligationID, &t)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("terms of obligation %s are not available to this org", obligationID)
	}
	return &t, nil
}

func checkDates(acceptanceDate int64, agreedCreditDays int, dueDate int64) error {
	if acceptanceDate <= 0 || dueDate < acceptanceDate {
		return fmt.Errorf("acceptanceDate is required and dueDate cannot be before it")
	}
	if agreedCreditDays < 0 || agreedCreditDays > 365 {
		return fmt.Errorf("agreedCreditDays must be 0 (no written agreement) to 365")
	}
	return nil
}

// ---- parties ----------------------------------------------------------------

// RegisterParty binds the calling identity to a buyer or supplier profile. A
// party registers itself; nobody can register on another's behalf, and one
// identity can be only one party.
func (c *SupplyChainContract) RegisterParty(ctx contractapi.TransactionContextInterface, partyID string, name string, role string, udyam string, class string, bankID string, accountHash string) error {
	msp, err := ctx.GetClientIdentity().GetMSPID()
	if err != nil {
		return fmt.Errorf("cannot read caller identity: %v", err)
	}
	if !validID(partyID) || name == "" {
		return fmt.Errorf("partyId must be 2-40 characters of A-Z, 0-9, _ or -, and name is required")
	}
	if !validID(bankID) || !validHash(accountHash) {
		return fmt.Errorf("bankId is required and accountHash must be a SHA-256 hex digest; account numbers are never stored")
	}
	switch role {
	case RoleBuyer:
		if msp != BuyerMSP {
			return fmt.Errorf("a buyer must act through %s, caller is %s", BuyerMSP, msp)
		}
		if udyam != "" || class != "" {
			return fmt.Errorf("udyam and class apply to suppliers only")
		}
	case RoleSupplier:
		if !udyamPattern.MatchString(udyam) {
			return fmt.Errorf("udyam must look like UDYAM-XX-00-0000000")
		}
		if class != ClassMicro && class != ClassSmall && class != ClassMedium {
			return fmt.Errorf("class must be %s, %s or %s", ClassMicro, ClassSmall, ClassMedium)
		}
	default:
		return fmt.Errorf("role must be %s or %s", RoleBuyer, RoleSupplier)
	}
	var existing Party
	if ok, err := getJSON(ctx, prefixParty+partyID, &existing); err != nil {
		return err
	} else if ok {
		return fmt.Errorf("party %s is already registered", partyID)
	}
	h, err := certHash(ctx)
	if err != nil {
		return err
	}
	var other string
	if ok, err := getJSON(ctx, prefixCert+h, &other); err != nil {
		return err
	} else if ok {
		return fmt.Errorf("this identity is already registered as party %s", other)
	}
	now, err := txTime(ctx)
	if err != nil {
		return err
	}
	if err := putJSON(ctx, prefixParty+partyID, Party{DocType: "party", PartyID: partyID, Name: name, Role: role,
		Udyam: udyam, Class: class, BankID: bankID, AccountHash: accountHash, MSPID: msp, CertHash: h, RegisteredAt: now}); err != nil {
		return err
	}
	return putJSON(ctx, prefixCert+h, partyID)
}

func (c *SupplyChainContract) GetParty(ctx contractapi.TransactionContextInterface, partyID string) (*Party, error) {
	return loadParty(ctx, partyID)
}

// ---- obligations ------------------------------------------------------------

func newObligation(ctx contractapi.TransactionContextInterface, id, chainID string, level int, debtor, approver string, creditor *Party, acceptanceDate int64, agreedCreditDays int, dueDate int64, status string, now int64) Obligation {
	covered, due := statutoryDue(acceptanceDate, agreedCreditDays, creditor.Class)
	o := Obligation{DocType: "obligation", ObligationID: id, ChainID: chainID, Level: level, DebtorID: debtor,
		CreditorID: creditor.PartyID, ApproverID: approver, Status: status, AcceptanceDate: acceptanceDate,
		AgreedCreditDays: agreedCreditDays, DueDate: dueDate, CreditorUdyam: creditor.Udyam, CreditorClass: creditor.Class,
		Covered43Bh: covered, StatutoryDueDate: due, RegisteredAt: now, TxID: ctx.GetStub().GetTxID()}
	if status == ObActive {
		o.ApprovedAt = now
	}
	return o
}

// CreateChain is called by the anchor buyer. It opens a chain with what the
// buyer owes its Tier-1 supplier. The amount and invoice reference travel as
// transient data under "terms".
func (c *SupplyChainContract) CreateChain(ctx contractapi.TransactionContextInterface, chainID string, obligationID string, tier1ID string, acceptanceDate int64, agreedCreditDays int, dueDate int64) (*Obligation, error) {
	buyer, err := callerParty(ctx)
	if err != nil {
		return nil, err
	}
	if buyer.Role != RoleBuyer {
		return nil, fmt.Errorf("only a buyer can open a chain; %s is a %s", buyer.PartyID, buyer.Role)
	}
	if !validID(chainID) || !validID(obligationID) {
		return nil, fmt.Errorf("chainId and obligationId must be 2-40 characters of A-Z, 0-9, _ or -")
	}
	var existing Chain
	if ok, err := getJSON(ctx, prefixChain+chainID, &existing); err != nil {
		return nil, err
	} else if ok {
		return nil, fmt.Errorf("chain %s already exists", chainID)
	}
	var dup Obligation
	if ok, err := getJSON(ctx, prefixOb+obligationID, &dup); err != nil {
		return nil, err
	} else if ok {
		return nil, fmt.Errorf("obligation %s already exists", obligationID)
	}
	tier1, err := loadParty(ctx, tier1ID)
	if err != nil {
		return nil, err
	}
	if tier1.Role != RoleSupplier {
		return nil, fmt.Errorf("%s is not a supplier", tier1ID)
	}
	if err := checkDates(acceptanceDate, agreedCreditDays, dueDate); err != nil {
		return nil, err
	}
	amount, invoiceRef, err := transientTerms(ctx)
	if err != nil {
		return nil, err
	}
	now, err := txTime(ctx)
	if err != nil {
		return nil, err
	}
	// The buyer has no tier above it, so its own obligation is active at once.
	ob := newObligation(ctx, obligationID, chainID, 1, buyer.PartyID, "", tier1, acceptanceDate, agreedCreditDays, dueDate, ObActive, now)
	if err := putJSON(ctx, prefixOb+obligationID, ob); err != nil {
		return nil, err
	}
	if err := putPrivate(ctx, keyTerms+obligationID, Terms{ObligationID: obligationID, Amount: amount, InvoiceRef: invoiceRef}); err != nil {
		return nil, err
	}
	chain := Chain{DocType: "chain", ChainID: chainID, BuyerID: buyer.PartyID, Status: ChainOpen,
		ObligationIDs: []string{obligationID}, PartyIDs: []string{buyer.PartyID, tier1.PartyID},
		CreatedAt: now, CreditedParties: []string{}}
	if err := putJSON(ctx, prefixChain+chainID, chain); err != nil {
		return nil, err
	}
	return &ob, nil
}

// RegisterObligation is called by a supplier to register what it owes its own
// downstream supplier. The caller is always the debtor: a tier can only commit
// its own money. It must already hold an approved place in the chain, and what
// it passes down in total cannot exceed what it is owed from above.
func (c *SupplyChainContract) RegisterObligation(ctx contractapi.TransactionContextInterface, obligationID string, chainID string, creditorID string, acceptanceDate int64, agreedCreditDays int, dueDate int64) (*Obligation, error) {
	debtor, err := callerParty(ctx)
	if err != nil {
		return nil, err
	}
	if debtor.Role != RoleSupplier {
		return nil, fmt.Errorf("a buyer's obligation is the chain itself; only suppliers register downstream obligations")
	}
	if !validID(obligationID) {
		return nil, fmt.Errorf("obligationId must be 2-40 characters of A-Z, 0-9, _ or -")
	}
	var dup Obligation
	if ok, err := getJSON(ctx, prefixOb+obligationID, &dup); err != nil {
		return nil, err
	} else if ok {
		return nil, fmt.Errorf("obligation %s already exists", obligationID)
	}
	chain, err := loadChain(ctx, chainID)
	if err != nil {
		return nil, err
	}
	if chain.Status != ChainOpen {
		return nil, fmt.Errorf("chain %s is %s; obligations must be registered before the buyer pays", chainID, chain.Status)
	}
	creditor, err := loadParty(ctx, creditorID)
	if err != nil {
		return nil, err
	}
	if creditor.Role != RoleSupplier {
		return nil, fmt.Errorf("%s is not a supplier", creditorID)
	}
	if contains(chain.PartyIDs, creditorID) {
		return nil, fmt.Errorf("%s already has a place in chain %s", creditorID, chainID)
	}
	if err := checkDates(acceptanceDate, agreedCreditDays, dueDate); err != nil {
		return nil, err
	}
	amount, invoiceRef, err := transientTerms(ctx)
	if err != nil {
		return nil, err
	}

	// Find what the caller is owed in this chain, and what it has already passed down.
	var parent *Obligation
	var passedDown int64
	for _, id := range chain.ObligationIDs {
		o, err := loadObligation(ctx, id)
		if err != nil {
			return nil, err
		}
		if o.CreditorID == debtor.PartyID && (o.Status == ObActive || o.Status == ObRegistered) {
			parent = o
		}
		if o.DebtorID == debtor.PartyID && (o.Status == ObActive || o.Status == ObRegistered) {
			t, err := loadTerms(ctx, id)
			if err != nil {
				return nil, err
			}
			passedDown += t.Amount
		}
	}
	if parent == nil {
		return nil, fmt.Errorf("%s is not owed anything in chain %s, so it has nothing to pass down", debtor.PartyID, chainID)
	}
	if parent.Status != ObActive {
		return nil, fmt.Errorf("%s's own place in chain %s has not been approved yet", debtor.PartyID, chainID)
	}
	parentTerms, err := loadTerms(ctx, parent.ObligationID)
	if err != nil {
		return nil, err
	}
	if passedDown+amount > parentTerms.Amount {
		return nil, fmt.Errorf("%s is owed %d paise in this chain and has already registered %d for its suppliers; it cannot pass down %d more", debtor.PartyID, parentTerms.Amount, passedDown, amount)
	}
	now, err := txTime(ctx)
	if err != nil {
		return nil, err
	}
	// The tier above the debtor must approve, because it is that tier's payment being split.
	ob := newObligation(ctx, obligationID, chainID, parent.Level+1, debtor.PartyID, parent.DebtorID, creditor, acceptanceDate, agreedCreditDays, dueDate, ObRegistered, now)
	if err := putJSON(ctx, prefixOb+obligationID, ob); err != nil {
		return nil, err
	}
	if err := putPrivate(ctx, keyTerms+obligationID, Terms{ObligationID: obligationID, Amount: amount, InvoiceRef: invoiceRef}); err != nil {
		return nil, err
	}
	chain.ObligationIDs = append(chain.ObligationIDs, obligationID)
	chain.PartyIDs = append(chain.PartyIDs, creditorID)
	if err := putJSON(ctx, prefixChain+chainID, chain); err != nil {
		return nil, err
	}
	return &ob, nil
}

// ApproveObligation is called by the tier above the debtor to accept that part
// of its payment will go straight to the debtor's supplier.
func (c *SupplyChainContract) ApproveObligation(ctx contractapi.TransactionContextInterface, obligationID string) (*Obligation, error) {
	caller, err := callerParty(ctx)
	if err != nil {
		return nil, err
	}
	ob, err := loadObligation(ctx, obligationID)
	if err != nil {
		return nil, err
	}
	if ob.ApproverID == "" || caller.PartyID != ob.ApproverID {
		return nil, fmt.Errorf("obligation %s can only be approved by %s, the tier above %s; caller is %s", obligationID, ob.ApproverID, ob.DebtorID, caller.PartyID)
	}
	if ob.Status != ObRegistered {
		return nil, fmt.Errorf("obligation %s is %s, not awaiting approval", obligationID, ob.Status)
	}
	chain, err := loadChain(ctx, ob.ChainID)
	if err != nil {
		return nil, err
	}
	if chain.Status != ChainOpen {
		return nil, fmt.Errorf("chain %s is already %s", ob.ChainID, chain.Status)
	}
	now, err := txTime(ctx)
	if err != nil {
		return nil, err
	}
	ob.Status, ob.ApprovedAt = ObActive, now
	if err := putJSON(ctx, prefixOb+obligationID, ob); err != nil {
		return nil, err
	}
	return ob, nil
}

// WithdrawObligation lets the debtor take back an obligation that has not been
// approved yet.
func (c *SupplyChainContract) WithdrawObligation(ctx contractapi.TransactionContextInterface, obligationID string) error {
	caller, err := callerParty(ctx)
	if err != nil {
		return err
	}
	ob, err := loadObligation(ctx, obligationID)
	if err != nil {
		return err
	}
	if caller.PartyID != ob.DebtorID {
		return fmt.Errorf("only %s, who registered obligation %s, can withdraw it", ob.DebtorID, obligationID)
	}
	if ob.Status != ObRegistered {
		return fmt.Errorf("obligation %s is %s and can no longer be withdrawn", obligationID, ob.Status)
	}
	chain, err := loadChain(ctx, ob.ChainID)
	if err != nil {
		return err
	}
	kept := []string{}
	for _, p := range chain.PartyIDs {
		if p != ob.CreditorID {
			kept = append(kept, p)
		}
	}
	chain.PartyIDs = kept
	ob.Status = ObWithdrawn
	if err := putJSON(ctx, prefixOb+obligationID, ob); err != nil {
		return err
	}
	return putJSON(ctx, prefixChain+ob.ChainID, chain)
}

// ReleasePayment is called by the buyer. In one transaction it works out every
// approved tier's net share of the buyer's payment, marks those obligations
// paid and records the split. Obligations still awaiting approval are left out:
// nobody above agreed to them, so that money goes to the debtor as usual.
func (c *SupplyChainContract) ReleasePayment(ctx contractapi.TransactionContextInterface, chainID string, paymentRef string) (*Chain, error) {
	caller, err := callerParty(ctx)
	if err != nil {
		return nil, err
	}
	chain, err := loadChain(ctx, chainID)
	if err != nil {
		return nil, err
	}
	if caller.PartyID != chain.BuyerID {
		return nil, fmt.Errorf("only %s, the buyer of chain %s, can release its payment; caller is %s", chain.BuyerID, chainID, caller.PartyID)
	}
	if chain.Status != ChainOpen {
		return nil, fmt.Errorf("duplicate: chain %s was already released with payment %s", chainID, chain.PaymentRef)
	}
	if !validID(paymentRef) {
		return nil, fmt.Errorf("paymentRef must be 2-40 characters of A-Z, 0-9, _ or -")
	}
	now, err := txTime(ctx)
	if err != nil {
		return nil, err
	}

	received := map[string]int64{}
	passed := map[string]int64{}
	level := map[string]int{}
	var total int64
	obs := make([]*Obligation, 0, len(chain.ObligationIDs))
	for _, id := range chain.ObligationIDs {
		o, err := loadObligation(ctx, id)
		if err != nil {
			return nil, err
		}
		obs = append(obs, o)
		if o.Status != ObActive {
			continue
		}
		t, err := loadTerms(ctx, id)
		if err != nil {
			return nil, err
		}
		received[o.CreditorID] += t.Amount
		level[o.CreditorID] = o.Level
		if o.Level == 1 {
			total += t.Amount
		} else {
			passed[o.DebtorID] += t.Amount
		}
	}

	rel := Release{ChainID: chainID, PaymentRef: paymentRef, Total: total, Credits: []Credit{}, ReleasedAt: now, TxID: ctx.GetStub().GetTxID()}
	creditors := make([]string, 0, len(received))
	for id := range received {
		creditors = append(creditors, id)
	}
	// Top of the chain first; a fixed order keeps the result identical on every peer.
	sort.Slice(creditors, func(i, j int) bool {
		if level[creditors[i]] != level[creditors[j]] {
			return level[creditors[i]] < level[creditors[j]]
		}
		return creditors[i] < creditors[j]
	})
	var sum int64
	for _, id := range creditors {
		p, err := loadParty(ctx, id)
		if err != nil {
			return nil, err
		}
		net := received[id] - passed[id]
		if net < 0 {
			return nil, fmt.Errorf("%s has passed down more than it is owed; chain %s cannot be released", id, chainID)
		}
		sum += net
		rel.Credits = append(rel.Credits, Credit{PartyID: id, Name: p.Name, Level: level[id], BankID: p.BankID,
			AccountHash: p.AccountHash, Received: received[id], PassedDown: passed[id], Net: net})
		chain.CreditedParties = append(chain.CreditedParties, id)
	}
	if sum != total {
		return nil, fmt.Errorf("split of %d paise does not add up to the buyer's payment of %d", sum, total)
	}

	for _, o := range obs {
		switch o.Status {
		case ObActive:
			o.Status, o.PaidAt, o.PaidOnTime = ObPaid, now, true
			if o.Covered43Bh && now > o.StatutoryDueDate {
				o.PaidOnTime = false
				o.DaysLate = (now - o.StatutoryDueDate + day - 1) / day
			}
		case ObRegistered:
			o.Status = ObExcluded
		default:
			continue
		}
		if err := putJSON(ctx, prefixOb+o.ObligationID, o); err != nil {
			return nil, err
		}
	}
	chain.Status, chain.ReleasedAt, chain.PaymentRef, chain.ReleaseTxID = ChainReleased, now, paymentRef, rel.TxID
	if err := putJSON(ctx, prefixChain+chainID, chain); err != nil {
		return nil, err
	}
	if err := putPrivate(ctx, keyRelease+chainID, rel); err != nil {
		return nil, err
	}
	// The event names who was paid, not how much.
	payload, err := json.Marshal(map[string]interface{}{"chainId": chainID, "paymentRef": paymentRef, "creditedParties": chain.CreditedParties})
	if err != nil {
		return nil, err
	}
	if err := ctx.GetStub().SetEvent(EventReleased, payload); err != nil {
		return nil, err
	}
	return chain, nil
}

// ---- queries ----------------------------------------------------------------

// GetChain returns a chain and the public part of its obligations.
func (c *SupplyChainContract) GetChain(ctx contractapi.TransactionContextInterface, chainID string) (*ChainView, error) {
	chain, err := loadChain(ctx, chainID)
	if err != nil {
		return nil, err
	}
	view := ChainView{Chain: *chain, Obligations: []Obligation{}}
	for _, id := range chain.ObligationIDs {
		o, err := loadObligation(ctx, id)
		if err != nil {
			return nil, err
		}
		view.Obligations = append(view.Obligations, *o)
	}
	return &view, nil
}

// GetTerms returns an obligation's amount and invoice reference. It fails for
// callers whose org is not a member of the private collection.
func (c *SupplyChainContract) GetTerms(ctx contractapi.TransactionContextInterface, obligationID string) (*Terms, error) {
	return loadTerms(ctx, obligationID)
}

// GetRelease returns how a released payment was split. Members only.
func (c *SupplyChainContract) GetRelease(ctx contractapi.TransactionContextInterface, chainID string) (*Release, error) {
	var r Release
	ok, err := getPrivate(ctx, keyRelease+chainID, &r)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("no release for chain %s is available to this org", chainID)
	}
	return &r, nil
}

// DeadlineView lists every unpaid obligation with its position against the
// Section 43B(h) payment deadline, most urgent first. asOf is a unix time, or 0
// for now. Obligations within warnDays of the deadline are flagged DUE_SOON.
func (c *SupplyChainContract) DeadlineView(ctx contractapi.TransactionContextInterface, asOf int64, warnDays int) ([]*DeadlineRow, error) {
	if warnDays < 0 {
		return nil, fmt.Errorf("warnDays cannot be negative")
	}
	if asOf == 0 {
		now, err := txTime(ctx)
		if err != nil {
			return nil, err
		}
		asOf = now
	}
	rows := []*DeadlineRow{}
	// Every prefix ends in ':', so the range ends just before the same prefix ending in ';'.
	it, err := ctx.GetStub().GetStateByRange(prefixOb, prefixOb[:len(prefixOb)-1]+";")
	if err != nil {
		return nil, fmt.Errorf("range read failed: %v", err)
	}
	defer it.Close()
	for it.HasNext() {
		kv, err := it.Next()
		if err != nil {
			return nil, err
		}
		var o Obligation
		if err := json.Unmarshal(kv.Value, &o); err != nil {
			return nil, err
		}
		if o.Status != ObRegistered && o.Status != ObActive {
			continue
		}
		flag, remaining := deadlineFlag(o.Covered43Bh, o.StatutoryDueDate, asOf, warnDays)
		row := &DeadlineRow{ObligationID: o.ObligationID, ChainID: o.ChainID, Level: o.Level, DebtorID: o.DebtorID,
			CreditorID: o.CreditorID, CreditorUdyam: o.CreditorUdyam, CreditorClass: o.CreditorClass, Status: o.Status,
			StatutoryDueDate: o.StatutoryDueDate, DaysRemaining: remaining, Flag: flag,
			DeductionAtRisk: flag == FlagOverdue || flag == FlagDueSoon, Amount: -1}
		var t Terms
		if ok, err := getPrivate(ctx, keyTerms+o.ObligationID, &t); err == nil && ok {
			row.Amount = t.Amount
		}
		rows = append(rows, row)
	}
	rank := map[string]int{FlagOverdue: 0, FlagDueSoon: 1, FlagOK: 2, FlagNotCovered: 3}
	sort.SliceStable(rows, func(i, j int) bool {
		if rank[rows[i].Flag] != rank[rows[j].Flag] {
			return rank[rows[i].Flag] < rank[rows[j].Flag]
		}
		if rows[i].DaysRemaining != rows[j].DaysRemaining {
			return rows[i].DaysRemaining < rows[j].DaysRemaining
		}
		return rows[i].ObligationID < rows[j].ObligationID
	})
	return rows, nil
}
