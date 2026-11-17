package main

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"strings"

	"github.com/hyperledger/fabric-contract-api-go/v2/contractapi"
)

// SettlementContract records every payment a switch processes, keyed by its
// UPI reference number (RRN), and settles merchants from a pre-funded float as
// soon as the sponsor bank confirms the money is in the aggregator's escrow.
//
// A payment that is debited but never confirmed is auto-reversed before the
// regulatory deadline. The request comes from an off-chain watcher, but the
// decision is made here from the payment's own on-chain timestamps.
//
// The chaincode endorsement policy is Aggregator AND SponsorBank: the two orgs
// that hold the escrow and the float. Who may call each function is enforced
// below from the caller's identity.
type SettlementContract struct {
	contractapi.Contract
}

const (
	SwitchMSP     = "SwitchOrgMSP"
	OperatorMSP   = "NPCIOrgMSP"
	AggregatorMSP = "AggregatorOrgMSP"
	SponsorMSP    = "SponsorBankOrgMSP"

	StatusDebited    = "DEBITED"    // payer debited, payee credit not yet confirmed
	StatusConfirmed  = "CONFIRMED"  // payee bank confirmed the credit
	StatusPaidOut    = "PAID_OUT"   // merchant paid from the float
	StatusReimbursed = "REIMBURSED" // float repaid from escrow at the settlement cycle
	StatusReversed   = "REVERSED"   // auto-reversed: never confirmed in time

	EventRecorded  = "PaymentRecorded"
	EventConfirmed = "CreditConfirmed"
	EventPaidOut   = "MerchantPaidOut"
	EventReversed  = "PaymentReversed"

	// FloatLanes splits the float into independent balances. A payout touches
	// one lane, chosen from the RRN, so concurrent payouts rarely write the
	// same key and block each other.
	FloatLanes = 32

	maxBatch = 100

	prefixBank     = "bank:"
	prefixMerchant = "merchant:"
	prefixPayment  = "pay:"
	prefixPending  = "pending:"
	prefixPayout   = "payout:"
	prefixReversal = "reversal:"
	prefixLane     = "lane:"
	prefixFunding  = "funding:"
	prefixCycle    = "cycle:"
	keyConfig      = "config"
)

// Config holds the turnaround-time rule. Only the network operator may change it.
type Config struct {
	// TatSeconds is how long after the debit an unconfirmed payment may stay
	// unresolved (RBI: T+1).
	TatSeconds int64 `json:"tatSeconds"`
	// ReverseMarginSeconds is how long before that deadline auto-reversal opens.
	ReverseMarginSeconds int64 `json:"reverseMarginSeconds"`
	// CompensationPaisePerDay is owed to the customer for each day past the deadline.
	CompensationPaisePerDay int64 `json:"compensationPaisePerDay"`
	// MaxClockSkewSeconds bounds how far in the future a claimed debit time may be.
	MaxClockSkewSeconds int64 `json:"maxClockSkewSeconds"`
}

func defaultConfig() Config {
	return Config{TatSeconds: 86400, ReverseMarginSeconds: 3600, CompensationPaisePerDay: 10000, MaxClockSkewSeconds: 5}
}

type Bank struct {
	DocType string `json:"docType"`
	BankID  string `json:"bankId"`
	MSPID   string `json:"mspId"`
}

type Merchant struct {
	DocType    string `json:"docType"`
	MerchantID string `json:"merchantId"`
	Name       string `json:"name"`
	FeeBps     int64  `json:"feeBps"` // aggregator fee on the gross amount
	GstBps     int64  `json:"gstBps"` // GST on the fee
	EscrowBank string `json:"escrowBank"`
	// AccountHash identifies the merchant's settlement account without storing it.
	AccountHash string `json:"accountHash"`
}

// Payment is the single record for one RRN. Amounts are in paise.
type Payment struct {
	DocType      string `json:"docType"`
	RRN          string `json:"rrn"`
	MerchantID   string `json:"merchantId"`
	PayerBank    string `json:"payerBank"`
	PayeeBank    string `json:"payeeBank"`
	Amount       int64  `json:"amount"`
	PayerRefHash string `json:"payerRefHash"`
	PayeeRefHash string `json:"payeeRefHash"`
	Status       string `json:"status"`
	DebitedAt    int64  `json:"debitedAt"`  // when the switch says the payer was debited
	RecordedAt   int64  `json:"recordedAt"` // timestamp of the recording transaction
	// EarliestAt is the earlier of the two times above. The deadline runs from
	// it, so a record that queued before reaching the ledger never gains time.
	EarliestAt   int64  `json:"earliestAt"`
	DeadlineAt   int64  `json:"deadlineAt"`
	ConfirmedAt  int64  `json:"confirmedAt"`
	PaidOutAt    int64  `json:"paidOutAt"`
	ReimbursedAt int64  `json:"reimbursedAt"`
	ReversedAt   int64  `json:"reversedAt"`
	TxID         string `json:"txId"`
}

type Pending struct {
	RRN        string `json:"rrn"`
	PayerBank  string `json:"payerBank"`
	Amount     int64  `json:"amount"`
	EarliestAt int64  `json:"earliestAt"`
	DeadlineAt int64  `json:"deadlineAt"`
}

// Payout is the record of one merchant settlement and its split.
type Payout struct {
	DocType        string `json:"docType"`
	RRN            string `json:"rrn"`
	MerchantID     string `json:"merchantId"`
	Gross          int64  `json:"gross"`
	Fee            int64  `json:"fee"`
	Gst            int64  `json:"gst"`
	MerchantAmount int64  `json:"merchantAmount"`
	Lane           int    `json:"lane"`
	ConfirmedAt    int64  `json:"confirmedAt"`
	PaidOutAt      int64  `json:"paidOutAt"`
	Reimbursed     bool   `json:"reimbursed"`
	CycleID        string `json:"cycleId"`
	TxID           string `json:"txId"`
}

// Reversal is the permanent record of an auto-reversal.
type Reversal struct {
	DocType               string `json:"docType"`
	RRN                   string `json:"rrn"`
	PayerBank             string `json:"payerBank"`
	Amount                int64  `json:"amount"`
	EarliestAt            int64  `json:"earliestAt"`
	DeadlineAt            int64  `json:"deadlineAt"`
	ReversedAt            int64  `json:"reversedAt"`
	WithinDeadline        bool   `json:"withinDeadline"`
	SecondsBeforeDeadline int64  `json:"secondsBeforeDeadline"` // negative if late
	CompensationPaise     int64  `json:"compensationPaise"`
	// FloatAdvanced is always false: only an unconfirmed payment can be
	// reversed, and an unconfirmed payment is never paid from the float.
	FloatAdvanced bool   `json:"floatAdvanced"`
	RequestedBy   string `json:"requestedBy"`
	TxID          string `json:"txId"`
}

type Lane struct {
	DocType string `json:"docType"`
	Lane    int    `json:"lane"`
	Balance int64  `json:"balance"`
}

type FloatStatus struct {
	Lanes      int   `json:"lanes"`
	Total      int64 `json:"total"`
	LowestLane int64 `json:"lowestLane"`
}

type Funding struct {
	DocType string `json:"docType"`
	Ref     string `json:"ref"`
	Amount  int64  `json:"amount"`
	At      int64  `json:"at"`
}

type Cycle struct {
	DocType        string `json:"docType"`
	CycleID        string `json:"cycleId"`
	Count          int    `json:"count"`
	TotalGross     int64  `json:"totalGross"`
	TotalFee       int64  `json:"totalFee"`
	TotalGst       int64  `json:"totalGst"`
	TotalReturned  int64  `json:"totalReturned"` // repaid to the float
	At             int64  `json:"at"`
	FloatAfterward int64  `json:"floatAfterward"`
}

// ---- pure rules -------------------------------------------------------------

// split divides a gross amount into the aggregator fee, GST on that fee, and
// what the merchant receives. Integer paise, rounded down, so the three parts
// always add back to the gross.
func split(gross, feeBps, gstBps int64) (fee, gst, merchant int64) {
	fee = gross * feeBps / 10000
	gst = fee * gstBps / 10000
	return fee, gst, gross - fee - gst
}

func laneFor(rrn string) int {
	h := fnv.New32a()
	h.Write([]byte(rrn))
	return int(h.Sum32() % FloatLanes)
}

func laneKey(i int) string { return fmt.Sprintf("%s%02d", prefixLane, i) }

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

// ---- helpers ----------------------------------------------------------------

func callerMSP(ctx contractapi.TransactionContextInterface) (string, error) {
	msp, err := ctx.GetClientIdentity().GetMSPID()
	if err != nil {
		return "", fmt.Errorf("cannot read caller identity: %v", err)
	}
	return msp, nil
}

func requireMSP(ctx contractapi.TransactionContextInterface, action string, allowed ...string) (string, error) {
	msp, err := callerMSP(ctx)
	if err != nil {
		return "", err
	}
	for _, a := range allowed {
		if msp == a {
			return msp, nil
		}
	}
	return "", fmt.Errorf("%s is not allowed to %s (allowed: %s)", msp, action, strings.Join(allowed, ", "))
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

func loadConfig(ctx contractapi.TransactionContextInterface) (Config, error) {
	cfg := defaultConfig()
	_, err := getJSON(ctx, keyConfig, &cfg)
	return cfg, err
}

func loadPayment(ctx contractapi.TransactionContextInterface, rrn string) (*Payment, error) {
	var p Payment
	ok, err := getJSON(ctx, prefixPayment+rrn, &p)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("payment %s is not recorded", rrn)
	}
	return &p, nil
}

// ---- setup ------------------------------------------------------------------

// SetConfig changes the turnaround-time rule. Network operator only.
func (c *SettlementContract) SetConfig(ctx contractapi.TransactionContextInterface, configJSON string) error {
	if _, err := requireMSP(ctx, "change the turnaround-time rule", OperatorMSP); err != nil {
		return err
	}
	cfg := defaultConfig()
	dec := json.NewDecoder(strings.NewReader(configJSON))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return fmt.Errorf("invalid config: %v", err)
	}
	if cfg.TatSeconds < 1 || cfg.ReverseMarginSeconds < 0 || cfg.ReverseMarginSeconds >= cfg.TatSeconds ||
		cfg.CompensationPaisePerDay < 0 || cfg.MaxClockSkewSeconds < 0 {
		return fmt.Errorf("tatSeconds must be positive, reverseMarginSeconds between 0 and tatSeconds, other values non-negative")
	}
	return putJSON(ctx, keyConfig, cfg)
}

func (c *SettlementContract) GetConfig(ctx contractapi.TransactionContextInterface) (*Config, error) {
	cfg, err := loadConfig(ctx)
	if err != nil {
		return nil, err
	}
	return &cfg, nil
}

// RegisterBank records which org speaks for a bank. Network operator only.
func (c *SettlementContract) RegisterBank(ctx contractapi.TransactionContextInterface, bankID string, mspID string) error {
	if _, err := requireMSP(ctx, "register banks", OperatorMSP); err != nil {
		return err
	}
	if !validID(bankID, 2) || mspID == "" {
		return fmt.Errorf("bankId must be 2-40 characters of A-Z, 0-9, _ or -, and mspId is required")
	}
	var existing Bank
	if ok, err := getJSON(ctx, prefixBank+bankID, &existing); err != nil {
		return err
	} else if ok {
		return fmt.Errorf("bank %s is already registered", bankID)
	}
	return putJSON(ctx, prefixBank+bankID, Bank{DocType: "bank", BankID: bankID, MSPID: mspID})
}

// RegisterMerchant onboards a merchant and its commercial terms. Aggregator only.
func (c *SettlementContract) RegisterMerchant(ctx contractapi.TransactionContextInterface, merchantID string, name string, feeBps int64, gstBps int64, escrowBank string, accountHash string) error {
	if _, err := requireMSP(ctx, "register merchants", AggregatorMSP); err != nil {
		return err
	}
	if !validID(merchantID, 2) || name == "" {
		return fmt.Errorf("merchantId must be 2-40 characters of A-Z, 0-9, _ or -, and name is required")
	}
	if feeBps < 0 || feeBps > 500 || gstBps < 0 || gstBps > 2800 {
		return fmt.Errorf("feeBps must be 0-500 and gstBps 0-2800")
	}
	if !validHash(accountHash) {
		return fmt.Errorf("accountHash must be a SHA-256 hex digest; account numbers are never stored")
	}
	var bank Bank
	if ok, err := getJSON(ctx, prefixBank+escrowBank, &bank); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("escrow bank %s is not registered", escrowBank)
	}
	if bank.MSPID != SponsorMSP {
		return fmt.Errorf("escrow bank %s is not operated by the sponsor bank org", escrowBank)
	}
	var existing Merchant
	if ok, err := getJSON(ctx, prefixMerchant+merchantID, &existing); err != nil {
		return err
	} else if ok {
		return fmt.Errorf("merchant %s is already registered", merchantID)
	}
	return putJSON(ctx, prefixMerchant+merchantID, Merchant{DocType: "merchant", MerchantID: merchantID, Name: name,
		FeeBps: feeBps, GstBps: gstBps, EscrowBank: escrowBank, AccountHash: accountHash})
}

func (c *SettlementContract) GetMerchant(ctx contractapi.TransactionContextInterface, merchantID string) (*Merchant, error) {
	var m Merchant
	ok, err := getJSON(ctx, prefixMerchant+merchantID, &m)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("merchant %s is not registered", merchantID)
	}
	return &m, nil
}

// FundFloat adds money to the settlement float. The sponsor bank calls it once
// it holds the aggregator's funds, so the aggregator cannot create float itself.
func (c *SettlementContract) FundFloat(ctx contractapi.TransactionContextInterface, amount int64, fundingRef string) (*FloatStatus, error) {
	if _, err := requireMSP(ctx, "fund the float", SponsorMSP); err != nil {
		return nil, err
	}
	if amount < FloatLanes {
		return nil, fmt.Errorf("amount must be at least %d paise", FloatLanes)
	}
	if !validID(fundingRef, 2) {
		return nil, fmt.Errorf("fundingRef must be 2-40 characters of A-Z, 0-9, _ or -")
	}
	var seen Funding
	if ok, err := getJSON(ctx, prefixFunding+fundingRef, &seen); err != nil {
		return nil, err
	} else if ok {
		return nil, fmt.Errorf("duplicate: funding %s is already recorded", fundingRef)
	}
	now, err := txTime(ctx)
	if err != nil {
		return nil, err
	}
	share, extra := amount/FloatLanes, amount%FloatLanes
	st := FloatStatus{Lanes: FloatLanes, LowestLane: -1}
	for i := 0; i < FloatLanes; i++ {
		lane := Lane{DocType: "lane", Lane: i}
		if _, err := getJSON(ctx, laneKey(i), &lane); err != nil {
			return nil, err
		}
		lane.Balance += share
		if i == 0 {
			lane.Balance += extra
		}
		if err := putJSON(ctx, laneKey(i), lane); err != nil {
			return nil, err
		}
		st.Total += lane.Balance
		if st.LowestLane < 0 || lane.Balance < st.LowestLane {
			st.LowestLane = lane.Balance
		}
	}
	if err := putJSON(ctx, prefixFunding+fundingRef, Funding{DocType: "funding", Ref: fundingRef, Amount: amount, At: now}); err != nil {
		return nil, err
	}
	return &st, nil
}

// GetFloat reports the float across all lanes.
func (c *SettlementContract) GetFloat(ctx contractapi.TransactionContextInterface) (*FloatStatus, error) {
	st := FloatStatus{Lanes: FloatLanes, LowestLane: -1}
	for i := 0; i < FloatLanes; i++ {
		var lane Lane
		if _, err := getJSON(ctx, laneKey(i), &lane); err != nil {
			return nil, err
		}
		st.Total += lane.Balance
		if st.LowestLane < 0 || lane.Balance < st.LowestLane {
			st.LowestLane = lane.Balance
		}
	}
	return &st, nil
}

// ---- payment lifecycle ------------------------------------------------------

// RecordPayment is called by the switch once the payer has been debited. The
// RRN is the key: a second record for the same RRN is rejected, which is what
// stops a retried request from being processed twice.
func (c *SettlementContract) RecordPayment(ctx contractapi.TransactionContextInterface, rrn string, merchantID string, payerBank string, payeeBank string, amount int64, debitedAt int64, payerRefHash string, payeeRefHash string) (*Payment, error) {
	if _, err := requireMSP(ctx, "record payments", SwitchMSP); err != nil {
		return nil, err
	}
	if !validID(rrn, 6) {
		return nil, fmt.Errorf("rrn must be 6-40 characters of A-Z, 0-9, _ or -")
	}
	var existing Payment
	if ok, err := getJSON(ctx, prefixPayment+rrn, &existing); err != nil {
		return nil, err
	} else if ok {
		return nil, fmt.Errorf("duplicate: payment %s is already recorded (status %s)", rrn, existing.Status)
	}
	if amount <= 0 {
		return nil, fmt.Errorf("amount must be positive")
	}
	if !validHash(payerRefHash) || !validHash(payeeRefHash) {
		return nil, fmt.Errorf("payer and payee references must be SHA-256 hex digests; account details are never stored")
	}
	for _, b := range []string{payerBank, payeeBank} {
		var bank Bank
		if ok, err := getJSON(ctx, prefixBank+b, &bank); err != nil {
			return nil, err
		} else if !ok {
			return nil, fmt.Errorf("bank %s is not registered", b)
		}
	}
	if merchantID != "" {
		var m Merchant
		if ok, err := getJSON(ctx, prefixMerchant+merchantID, &m); err != nil {
			return nil, err
		} else if !ok {
			return nil, fmt.Errorf("merchant %s is not registered", merchantID)
		}
		if m.EscrowBank != payeeBank {
			return nil, fmt.Errorf("merchant %s is settled through escrow at %s, not %s", merchantID, m.EscrowBank, payeeBank)
		}
	}
	cfg, err := loadConfig(ctx)
	if err != nil {
		return nil, err
	}
	now, err := txTime(ctx)
	if err != nil {
		return nil, err
	}
	if debitedAt <= 0 || debitedAt > now+cfg.MaxClockSkewSeconds {
		return nil, fmt.Errorf("debitedAt %d is in the future (transaction time %d)", debitedAt, now)
	}
	earliest := debitedAt
	if now < earliest {
		earliest = now
	}
	p := Payment{DocType: "payment", RRN: rrn, MerchantID: merchantID, PayerBank: payerBank, PayeeBank: payeeBank,
		Amount: amount, PayerRefHash: payerRefHash, PayeeRefHash: payeeRefHash, Status: StatusDebited,
		DebitedAt: debitedAt, RecordedAt: now, EarliestAt: earliest, DeadlineAt: earliest + cfg.TatSeconds,
		TxID: ctx.GetStub().GetTxID()}
	if err := putJSON(ctx, prefixPayment+rrn, p); err != nil {
		return nil, err
	}
	pend := Pending{RRN: rrn, PayerBank: payerBank, Amount: amount, EarliestAt: earliest, DeadlineAt: p.DeadlineAt}
	if err := putJSON(ctx, prefixPending+rrn, pend); err != nil {
		return nil, err
	}
	if err := emit(ctx, EventRecorded, pend); err != nil {
		return nil, err
	}
	return &p, nil
}

// ConfirmCredit is called by the payee bank's org when the credit has landed.
// For a merchant payment the payee account is the aggregator's escrow, so this
// is the sponsor bank confirming the escrow credit.
func (c *SettlementContract) ConfirmCredit(ctx contractapi.TransactionContextInterface, rrn string) (*Payment, error) {
	msp, err := callerMSP(ctx)
	if err != nil {
		return nil, err
	}
	p, err := loadPayment(ctx, rrn)
	if err != nil {
		return nil, err
	}
	var bank Bank
	if ok, err := getJSON(ctx, prefixBank+p.PayeeBank, &bank); err != nil {
		return nil, err
	} else if !ok {
		return nil, fmt.Errorf("payee bank %s is not registered", p.PayeeBank)
	}
	if msp != bank.MSPID {
		return nil, fmt.Errorf("only the payee bank's org (%s) may confirm the credit for %s, caller is %s", bank.MSPID, rrn, msp)
	}
	switch p.Status {
	case StatusDebited:
	case StatusReversed:
		return nil, fmt.Errorf("payment %s was auto-reversed at %d; the credit must be returned, not confirmed", rrn, p.ReversedAt)
	default:
		return nil, fmt.Errorf("duplicate: payment %s is already %s", rrn, p.Status)
	}
	now, err := txTime(ctx)
	if err != nil {
		return nil, err
	}
	p.Status, p.ConfirmedAt = StatusConfirmed, now
	if err := putJSON(ctx, prefixPayment+rrn, p); err != nil {
		return nil, err
	}
	if err := ctx.GetStub().DelState(prefixPending + rrn); err != nil {
		return nil, err
	}
	if err := emit(ctx, EventConfirmed, map[string]interface{}{"rrn": rrn, "merchantId": p.MerchantID, "amount": p.Amount, "confirmedAt": now}); err != nil {
		return nil, err
	}
	return p, nil
}

// PayoutMerchant pays a merchant from the float. In one transaction it checks
// that the escrow credit is confirmed, checks the float, deducts the merchant's
// share and records the payout and its split. Aggregator only.
func (c *SettlementContract) PayoutMerchant(ctx contractapi.TransactionContextInterface, rrn string) (*Payout, error) {
	if _, err := requireMSP(ctx, "request merchant payouts", AggregatorMSP); err != nil {
		return nil, err
	}
	var prior Payout
	if ok, err := getJSON(ctx, prefixPayout+rrn, &prior); err != nil {
		return nil, err
	} else if ok {
		return nil, fmt.Errorf("duplicate: payment %s was already paid out at %d", rrn, prior.PaidOutAt)
	}
	p, err := loadPayment(ctx, rrn)
	if err != nil {
		return nil, err
	}
	if p.MerchantID == "" {
		return nil, fmt.Errorf("payment %s is not a merchant payment", rrn)
	}
	if p.Status != StatusConfirmed {
		return nil, fmt.Errorf("payment %s is %s; a payout needs the escrow credit to be confirmed first", rrn, p.Status)
	}
	var m Merchant
	if ok, err := getJSON(ctx, prefixMerchant+p.MerchantID, &m); err != nil {
		return nil, err
	} else if !ok {
		return nil, fmt.Errorf("merchant %s is not registered", p.MerchantID)
	}
	fee, gst, net := split(p.Amount, m.FeeBps, m.GstBps)

	// Use the payment's own lane; fall through to the next ones only if it is short.
	start := laneFor(rrn)
	chosen := -1
	var lane Lane
	for i := 0; i < FloatLanes; i++ {
		n := (start + i) % FloatLanes
		var l Lane
		if _, err := getJSON(ctx, laneKey(n), &l); err != nil {
			return nil, err
		}
		if l.Balance >= net {
			chosen, lane = n, l
			break
		}
	}
	if chosen < 0 {
		return nil, fmt.Errorf("float exhausted: no lane holds %d paise for payment %s", net, rrn)
	}
	lane.DocType, lane.Lane = "lane", chosen
	lane.Balance -= net
	if err := putJSON(ctx, laneKey(chosen), lane); err != nil {
		return nil, err
	}

	now, err := txTime(ctx)
	if err != nil {
		return nil, err
	}
	out := Payout{DocType: "payout", RRN: rrn, MerchantID: p.MerchantID, Gross: p.Amount, Fee: fee, Gst: gst,
		MerchantAmount: net, Lane: chosen, ConfirmedAt: p.ConfirmedAt, PaidOutAt: now, TxID: ctx.GetStub().GetTxID()}
	if err := putJSON(ctx, prefixPayout+rrn, out); err != nil {
		return nil, err
	}
	p.Status, p.PaidOutAt = StatusPaidOut, now
	if err := putJSON(ctx, prefixPayment+rrn, p); err != nil {
		return nil, err
	}
	if err := emit(ctx, EventPaidOut, out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ReimburseFloat is called by the sponsor bank when the aggregator's normal
// settlement cycle releases escrow funds. Each listed payout's advance goes
// back to the lane it came from; the fee and GST stay with the aggregator.
func (c *SettlementContract) ReimburseFloat(ctx contractapi.TransactionContextInterface, cycleID string, rrnsJSON string) (*Cycle, error) {
	if _, err := requireMSP(ctx, "reimburse the float", SponsorMSP); err != nil {
		return nil, err
	}
	if !validID(cycleID, 2) {
		return nil, fmt.Errorf("cycleId must be 2-40 characters of A-Z, 0-9, _ or -")
	}
	var seen Cycle
	if ok, err := getJSON(ctx, prefixCycle+cycleID, &seen); err != nil {
		return nil, err
	} else if ok {
		return nil, fmt.Errorf("duplicate: settlement cycle %s is already recorded", cycleID)
	}
	var rrns []string
	if err := json.Unmarshal([]byte(rrnsJSON), &rrns); err != nil {
		return nil, fmt.Errorf("rrns must be a JSON array of strings: %v", err)
	}
	if len(rrns) == 0 || len(rrns) > maxBatch {
		return nil, fmt.Errorf("a cycle call covers 1 to %d payments, got %d", maxBatch, len(rrns))
	}
	now, err := txTime(ctx)
	if err != nil {
		return nil, err
	}
	inBatch := map[string]bool{}
	for _, rrn := range rrns {
		if inBatch[rrn] {
			return nil, fmt.Errorf("payment %s is listed twice", rrn)
		}
		inBatch[rrn] = true
	}
	cycle := Cycle{DocType: "cycle", CycleID: cycleID, At: now}
	lanes := map[int]*Lane{}
	for _, rrn := range rrns {
		var out Payout
		if ok, err := getJSON(ctx, prefixPayout+rrn, &out); err != nil {
			return nil, err
		} else if !ok {
			return nil, fmt.Errorf("payment %s has no payout to reimburse", rrn)
		}
		if out.Reimbursed {
			return nil, fmt.Errorf("duplicate: payout %s was already reimbursed in cycle %s", rrn, out.CycleID)
		}
		l := lanes[out.Lane]
		if l == nil {
			l = &Lane{DocType: "lane", Lane: out.Lane}
			if _, err := getJSON(ctx, laneKey(out.Lane), l); err != nil {
				return nil, err
			}
			lanes[out.Lane] = l
		}
		l.Balance += out.MerchantAmount
		out.Reimbursed, out.CycleID = true, cycleID
		if err := putJSON(ctx, prefixPayout+rrn, out); err != nil {
			return nil, err
		}
		p, err := loadPayment(ctx, rrn)
		if err != nil {
			return nil, err
		}
		p.Status, p.ReimbursedAt = StatusReimbursed, now
		if err := putJSON(ctx, prefixPayment+rrn, p); err != nil {
			return nil, err
		}
		cycle.Count++
		cycle.TotalGross += out.Gross
		cycle.TotalFee += out.Fee
		cycle.TotalGst += out.Gst
		cycle.TotalReturned += out.MerchantAmount
	}
	for i := 0; i < FloatLanes; i++ { // fixed order keeps the write set identical on every peer
		if l := lanes[i]; l != nil {
			if err := putJSON(ctx, laneKey(i), l); err != nil {
				return nil, err
			}
		}
	}
	if err := putJSON(ctx, prefixCycle+cycleID, cycle); err != nil {
		return nil, err
	}
	return &cycle, nil
}

// RequestReversal is submitted by the off-chain watcher when an unconfirmed
// payment nears its deadline. The watcher only asks: this function decides,
// from the payment's own on-chain timestamps, whether the reversal window has
// opened, and writes a permanent record if it reverses.
func (c *SettlementContract) RequestReversal(ctx contractapi.TransactionContextInterface, rrn string) (*Reversal, error) {
	msp, err := requireMSP(ctx, "request a reversal", OperatorMSP, SwitchMSP)
	if err != nil {
		return nil, err
	}
	p, err := loadPayment(ctx, rrn)
	if err != nil {
		return nil, err
	}
	if p.Status != StatusDebited {
		return nil, fmt.Errorf("payment %s is %s; only a debited, unconfirmed payment can be reversed", rrn, p.Status)
	}
	var paid Payout
	if ok, err := getJSON(ctx, prefixPayout+rrn, &paid); err != nil {
		return nil, err
	} else if ok {
		return nil, fmt.Errorf("payment %s has a float payout and cannot be reversed here", rrn)
	}
	cfg, err := loadConfig(ctx)
	if err != nil {
		return nil, err
	}
	now, err := txTime(ctx)
	if err != nil {
		return nil, err
	}
	// The deadline is fixed when the payment is recorded; a later rule change
	// does not move it. Only the margin before it is read from current config.
	opensAt := p.DeadlineAt - cfg.ReverseMarginSeconds
	if now < opensAt {
		return nil, fmt.Errorf("too early: the reversal window for %s opens in %ds (deadline in %ds)", rrn, opensAt-now, p.DeadlineAt-now)
	}
	rev := Reversal{DocType: "reversal", RRN: rrn, PayerBank: p.PayerBank, Amount: p.Amount,
		EarliestAt: p.EarliestAt, DeadlineAt: p.DeadlineAt, ReversedAt: now,
		WithinDeadline: now <= p.DeadlineAt, SecondsBeforeDeadline: p.DeadlineAt - now,
		FloatAdvanced: false, RequestedBy: msp, TxID: ctx.GetStub().GetTxID()}
	if !rev.WithinDeadline {
		daysLate := (now - p.DeadlineAt + 86399) / 86400
		rev.CompensationPaise = daysLate * cfg.CompensationPaisePerDay
	}
	p.Status, p.ReversedAt = StatusReversed, now
	if err := putJSON(ctx, prefixPayment+rrn, p); err != nil {
		return nil, err
	}
	if err := putJSON(ctx, prefixReversal+rrn, rev); err != nil {
		return nil, err
	}
	if err := ctx.GetStub().DelState(prefixPending + rrn); err != nil {
		return nil, err
	}
	if err := emit(ctx, EventReversed, rev); err != nil {
		return nil, err
	}
	return &rev, nil
}

// ---- queries ----------------------------------------------------------------

func (c *SettlementContract) GetPayment(ctx contractapi.TransactionContextInterface, rrn string) (*Payment, error) {
	return loadPayment(ctx, rrn)
}

func (c *SettlementContract) GetPayout(ctx contractapi.TransactionContextInterface, rrn string) (*Payout, error) {
	var out Payout
	ok, err := getJSON(ctx, prefixPayout+rrn, &out)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("payment %s has no payout", rrn)
	}
	return &out, nil
}

func (c *SettlementContract) GetReversal(ctx contractapi.TransactionContextInterface, rrn string) (*Reversal, error) {
	var rev Reversal
	ok, err := getJSON(ctx, prefixReversal+rrn, &rev)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("payment %s has no reversal", rrn)
	}
	return &rev, nil
}

func (c *SettlementContract) GetCycle(ctx contractapi.TransactionContextInterface, cycleID string) (*Cycle, error) {
	var cy Cycle
	ok, err := getJSON(ctx, prefixCycle+cycleID, &cy)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("settlement cycle %s is not recorded", cycleID)
	}
	return &cy, nil
}

// GetPendingPayments lists payments that are debited but not yet confirmed,
// up to 500. The watcher uses it to rebuild its view after a restart.
func (c *SettlementContract) GetPendingPayments(ctx contractapi.TransactionContextInterface) ([]*Pending, error) {
	out := []*Pending{}
	// Every prefix ends in ':', so the range ends just before the same prefix ending in ';'.
	end := prefixPending[:len(prefixPending)-1] + ";"
	it, err := ctx.GetStub().GetStateByRange(prefixPending, end)
	if err != nil {
		return nil, fmt.Errorf("range read failed: %v", err)
	}
	defer it.Close()
	for it.HasNext() && len(out) < 500 {
		kv, err := it.Next()
		if err != nil {
			return nil, err
		}
		var p Pending
		if err := json.Unmarshal(kv.Value, &p); err != nil {
			return nil, err
		}
		out = append(out, &p)
	}
	return out, nil
}
