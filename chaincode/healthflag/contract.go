package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hyperledger/fabric-contract-api-go/v2/contractapi"
)

// HealthContract keeps one shared, switch-measured health flag per bank.
//
// A switch and the network operator each record what they observe for a bank.
// A switch may then propose a flag change, and the contract accepts it only if
// the operator's own recent observation agrees. Together with the chaincode
// endorsement policy (switch AND operator) this means no single switch can
// flag a bank, and the bank being measured can neither block nor alter its flag.
type HealthContract struct {
	contractapi.Contract
}

const (
	// Orgs allowed to act. The bank being measured is deliberately absent.
	SwitchMSP   = "SwitchOrgMSP"
	OperatorMSP = "NPCIOrgMSP"

	StatusHealthy  = "HEALTHY"
	StatusDegraded = "DEGRADED"
	// StatusProbing: cool-down has passed and switches send a small share of
	// live traffic; the flag returns to HEALTHY only if those payments succeed.
	StatusProbing = "PROBING"

	ModeFailFast  = "FAIL_FAST"
	ModeHoldRetry = "HOLD_RETRY"

	// EventFlagChanged is emitted on every flag change; switches subscribe to
	// it to refresh their local routing cache.
	EventFlagChanged = "BankFlagChanged"

	// Key prefixes. Plain prefixes (no composite keys) so the keys are valid
	// text in the SQL state database and range scans stay simple.
	prefixBank = "bank:"
	prefixFlag = "flag:"
	prefixObs  = "obs:"
	prefixLog  = "log:"
	keyConfig  = "config"
)

// Config holds the thresholds every endorsing peer applies identically.
type Config struct {
	MinSample        int `json:"minSample"`        // payments needed before a window counts
	TimeoutRateBps   int `json:"timeoutRateBps"`   // degraded at or above (basis points)
	FailureRateBps   int `json:"failureRateBps"`   // degraded at or above (basis points)
	P95LatencyMs     int `json:"p95LatencyMs"`     // degraded at or above
	FreshnessSeconds int `json:"freshnessSeconds"` // max age of an observation used as evidence
	CooldownSeconds  int `json:"cooldownSeconds"`  // minimum time DEGRADED before probing
	ProbeShareBps    int `json:"probeShareBps"`    // share of live traffic sent while PROBING
	ProbeMinSample   int `json:"probeMinSample"`   // probe payments needed to judge recovery
}

func defaultConfig() Config {
	return Config{
		MinSample:        20,
		TimeoutRateBps:   500,
		FailureRateBps:   1000,
		P95LatencyMs:     3000,
		FreshnessSeconds: 30,
		CooldownSeconds:  30,
		ProbeShareBps:    500,
		ProbeMinSample:   10,
	}
}

// Bank is a registered bank and the behaviour it has chosen for itself when degraded.
type Bank struct {
	BankID string `json:"bankId"`
	Name   string `json:"name"`
	MSPID  string `json:"mspId"`
	Mode   string `json:"mode"`   // FAIL_FAST or HOLD_RETRY
	HoldMs int    `json:"holdMs"` // how long a switch may hold a payment in HOLD_RETRY
}

// Observation is one observer's measurement of a bank over a time window.
type Observation struct {
	BankID        string `json:"bankId"`
	ObserverMSP   string `json:"observerMsp"`
	WindowSeconds int    `json:"windowSeconds"`
	TxCount       int    `json:"txCount"`
	TimeoutCount  int    `json:"timeoutCount"`
	FailureCount  int    `json:"failureCount"`
	P95LatencyMs  int    `json:"p95LatencyMs"`
	RecordedAt    int64  `json:"recordedAt"` // transaction timestamp, unix seconds
	TxID          string `json:"txId"`
}

// BankFlag is the shared flag switches route on.
type BankFlag struct {
	BankID        string      `json:"bankId"`
	Status        string      `json:"status"`
	Since         int64       `json:"since"` // transaction timestamp of this change, unix seconds
	Seq           int         `json:"seq"`
	ProposedBy    string      `json:"proposedBy"`
	Reason        string      `json:"reason"`
	Mode          string      `json:"mode"`
	HoldMs        int         `json:"holdMs"`
	ProbeShareBps int         `json:"probeShareBps"`
	SwitchObs     Observation `json:"switchObs"`
	OperatorObs   Observation `json:"operatorObs"`
	TxID          string      `json:"txId"`
}

// ---- pure rules -------------------------------------------------------------

// classify turns a measurement into HEALTHY or DEGRADED. A window with too few
// payments is not evidence of degradation.
func classify(o Observation, minSample int, cfg Config) string {
	if o.TxCount < minSample || o.TxCount == 0 {
		return StatusHealthy
	}
	timeoutBps := o.TimeoutCount * 10000 / o.TxCount
	failureBps := o.FailureCount * 10000 / o.TxCount
	if timeoutBps >= cfg.TimeoutRateBps || failureBps >= cfg.FailureRateBps || o.P95LatencyMs >= cfg.P95LatencyMs {
		return StatusDegraded
	}
	return StatusHealthy
}

func validateConfig(c Config) error {
	if c.MinSample < 1 || c.ProbeMinSample < 1 {
		return fmt.Errorf("minSample and probeMinSample must be at least 1")
	}
	for name, v := range map[string]int{"timeoutRateBps": c.TimeoutRateBps, "failureRateBps": c.FailureRateBps, "probeShareBps": c.ProbeShareBps} {
		if v < 1 || v > 10000 {
			return fmt.Errorf("%s must be between 1 and 10000 basis points", name)
		}
	}
	if c.P95LatencyMs < 1 || c.FreshnessSeconds < 1 || c.CooldownSeconds < 0 {
		return fmt.Errorf("p95LatencyMs and freshnessSeconds must be positive and cooldownSeconds non-negative")
	}
	return nil
}

func validID(id string) bool {
	if id == "" || len(id) > 32 {
		return false
	}
	for _, r := range id {
		if !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '_' {
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

func (c *HealthContract) loadConfig(ctx contractapi.TransactionContextInterface) (Config, error) {
	cfg := defaultConfig()
	if _, err := getJSON(ctx, keyConfig, &cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func (c *HealthContract) loadBank(ctx contractapi.TransactionContextInterface, bankID string) (*Bank, error) {
	var b Bank
	ok, err := getJSON(ctx, prefixBank+bankID, &b)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("bank %s is not registered", bankID)
	}
	return &b, nil
}

// writeFlag stores the flag, appends it to the bank's audit log and emits the event.
func (c *HealthContract) writeFlag(ctx contractapi.TransactionContextInterface, f *BankFlag) error {
	if err := putJSON(ctx, prefixFlag+f.BankID, f); err != nil {
		return err
	}
	if err := putJSON(ctx, fmt.Sprintf("%s%s:%010d", prefixLog, f.BankID, f.Seq), f); err != nil {
		return err
	}
	payload, err := json.Marshal(f)
	if err != nil {
		return err
	}
	return ctx.GetStub().SetEvent(EventFlagChanged, payload)
}

// ---- transactions -----------------------------------------------------------

// SetConfig replaces the thresholds. Only the network operator may call it.
func (c *HealthContract) SetConfig(ctx contractapi.TransactionContextInterface, configJSON string) error {
	msp, err := callerMSP(ctx)
	if err != nil {
		return err
	}
	if msp != OperatorMSP {
		return fmt.Errorf("only the network operator (%s) may change thresholds, caller is %s", OperatorMSP, msp)
	}
	cfg := defaultConfig()
	dec := json.NewDecoder(strings.NewReader(configJSON))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return fmt.Errorf("invalid config: %v", err)
	}
	if err := validateConfig(cfg); err != nil {
		return err
	}
	return putJSON(ctx, keyConfig, cfg)
}

// GetConfig returns the thresholds in force.
func (c *HealthContract) GetConfig(ctx contractapi.TransactionContextInterface) (*Config, error) {
	cfg, err := c.loadConfig(ctx)
	if err != nil {
		return nil, err
	}
	return &cfg, nil
}

// RegisterBank adds a bank and starts it HEALTHY. Only the network operator may call it.
func (c *HealthContract) RegisterBank(ctx contractapi.TransactionContextInterface, bankID string, name string, mspID string) error {
	msp, err := callerMSP(ctx)
	if err != nil {
		return err
	}
	if msp != OperatorMSP {
		return fmt.Errorf("only the network operator (%s) may register banks, caller is %s", OperatorMSP, msp)
	}
	if !validID(bankID) {
		return fmt.Errorf("bankId must be 1-32 characters of A-Z, 0-9 or _")
	}
	if name == "" || mspID == "" {
		return fmt.Errorf("name and mspId are required")
	}
	var existing Bank
	if ok, err := getJSON(ctx, prefixBank+bankID, &existing); err != nil {
		return err
	} else if ok {
		return fmt.Errorf("bank %s is already registered", bankID)
	}
	now, err := txTime(ctx)
	if err != nil {
		return err
	}
	cfg, err := c.loadConfig(ctx)
	if err != nil {
		return err
	}
	bank := Bank{BankID: bankID, Name: name, MSPID: mspID, Mode: ModeFailFast, HoldMs: 0}
	if err := putJSON(ctx, prefixBank+bankID, bank); err != nil {
		return err
	}
	return c.writeFlag(ctx, &BankFlag{
		BankID: bankID, Status: StatusHealthy, Since: now, Seq: 0, ProposedBy: msp,
		Reason: "registered", Mode: bank.Mode, HoldMs: bank.HoldMs, ProbeShareBps: cfg.ProbeShareBps,
		TxID: ctx.GetStub().GetTxID(),
	})
}

// SetBankMode lets a bank choose what switches do with its payments while it is
// degraded: fail them at once, or hold and retry for up to holdMs. Only the
// bank's own org may call it. This does not let the bank touch its flag status.
func (c *HealthContract) SetBankMode(ctx contractapi.TransactionContextInterface, bankID string, mode string, holdMs int) error {
	msp, err := callerMSP(ctx)
	if err != nil {
		return err
	}
	bank, err := c.loadBank(ctx, bankID)
	if err != nil {
		return err
	}
	if msp != bank.MSPID {
		return fmt.Errorf("only %s may configure bank %s, caller is %s", bank.MSPID, bankID, msp)
	}
	switch mode {
	case ModeFailFast:
		holdMs = 0
	case ModeHoldRetry:
		if holdMs < 1 || holdMs > 30000 {
			return fmt.Errorf("holdMs must be between 1 and 30000 for %s", ModeHoldRetry)
		}
	default:
		return fmt.Errorf("mode must be %s or %s", ModeFailFast, ModeHoldRetry)
	}
	bank.Mode, bank.HoldMs = mode, holdMs
	if err := putJSON(ctx, prefixBank+bankID, bank); err != nil {
		return err
	}
	var flag BankFlag
	if ok, err := getJSON(ctx, prefixFlag+bankID, &flag); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("bank %s has no flag", bankID)
	}
	// Status and Since are untouched: only the bank's own routing preference changes.
	flag.Mode, flag.HoldMs = mode, holdMs
	flag.Seq++
	flag.ProposedBy = msp
	flag.Reason = "bank changed degraded-mode behaviour"
	flag.TxID = ctx.GetStub().GetTxID()
	return c.writeFlag(ctx, &flag)
}

// SubmitObservation records what the calling org measured for a bank over its
// latest window. Only a switch or the network operator may call it; each keeps
// its own latest observation per bank.
func (c *HealthContract) SubmitObservation(ctx contractapi.TransactionContextInterface, bankID string, windowSeconds int, txCount int, timeoutCount int, failureCount int, p95LatencyMs int) error {
	msp, err := callerMSP(ctx)
	if err != nil {
		return err
	}
	if msp != SwitchMSP && msp != OperatorMSP {
		return fmt.Errorf("only a switch (%s) or the network operator (%s) may submit observations, caller is %s", SwitchMSP, OperatorMSP, msp)
	}
	if _, err := c.loadBank(ctx, bankID); err != nil {
		return err
	}
	if windowSeconds < 1 || txCount < 0 || timeoutCount < 0 || failureCount < 0 || p95LatencyMs < 0 {
		return fmt.Errorf("windowSeconds must be positive and counts non-negative")
	}
	if timeoutCount+failureCount > txCount {
		return fmt.Errorf("timeouts plus failures (%d) exceed payments observed (%d)", timeoutCount+failureCount, txCount)
	}
	now, err := txTime(ctx)
	if err != nil {
		return err
	}
	return putJSON(ctx, prefixObs+bankID+":"+msp, Observation{
		BankID: bankID, ObserverMSP: msp, WindowSeconds: windowSeconds, TxCount: txCount,
		TimeoutCount: timeoutCount, FailureCount: failureCount, P95LatencyMs: p95LatencyMs,
		RecordedAt: now, TxID: ctx.GetStub().GetTxID(),
	})
}

// ProposeFlag is called by a switch to move a bank's flag. Allowed moves:
//
//	HEALTHY or PROBING -> DEGRADED  when the switch's and the operator's fresh observations both show degradation
//	DEGRADED -> PROBING             once the cool-down has elapsed
//	PROBING -> HEALTHY              when both observations, taken since probing began, show the probe payments succeeding
//
// The operator's agreement is checked here from its own on-chain observation,
// and again by the endorsement policy, which requires the operator's peer to sign.
func (c *HealthContract) ProposeFlag(ctx contractapi.TransactionContextInterface, bankID string, status string, reason string) (*BankFlag, error) {
	msp, err := callerMSP(ctx)
	if err != nil {
		return nil, err
	}
	if msp != SwitchMSP {
		return nil, fmt.Errorf("only a switch (%s) may propose a flag change, caller is %s", SwitchMSP, msp)
	}
	bank, err := c.loadBank(ctx, bankID)
	if err != nil {
		return nil, err
	}
	var flag BankFlag
	if ok, err := getJSON(ctx, prefixFlag+bankID, &flag); err != nil {
		return nil, err
	} else if !ok {
		return nil, fmt.Errorf("bank %s has no flag", bankID)
	}
	cfg, err := c.loadConfig(ctx)
	if err != nil {
		return nil, err
	}
	now, err := txTime(ctx)
	if err != nil {
		return nil, err
	}

	var swObs, opObs Observation
	needEvidence := status == StatusDegraded || status == StatusHealthy
	if needEvidence {
		for _, e := range []struct {
			who string
			msp string
			out *Observation
		}{{"switch", msp, &swObs}, {"network operator", OperatorMSP, &opObs}} {
			ok, err := getJSON(ctx, prefixObs+bankID+":"+e.msp, e.out)
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, fmt.Errorf("no %s observation on record for bank %s", e.who, bankID)
			}
			if age := now - e.out.RecordedAt; age > int64(cfg.FreshnessSeconds) || age < -int64(cfg.FreshnessSeconds) {
				return nil, fmt.Errorf("%s observation for bank %s is stale (%ds old, limit %ds)", e.who, bankID, age, cfg.FreshnessSeconds)
			}
		}
	}

	switch status {
	case StatusDegraded:
		if flag.Status != StatusHealthy && flag.Status != StatusProbing {
			return nil, fmt.Errorf("bank %s is already %s", bankID, flag.Status)
		}
		// While probing only a trickle of traffic flows, so judge it on the probe sample size.
		minSample := cfg.MinSample
		if flag.Status == StatusProbing {
			minSample = cfg.ProbeMinSample
		}
		if classify(swObs, minSample, cfg) != StatusDegraded {
			return nil, fmt.Errorf("the switch's own observation does not show bank %s degraded", bankID)
		}
		if classify(opObs, minSample, cfg) != StatusDegraded {
			return nil, fmt.Errorf("the network operator's observation does not show bank %s degraded; flag unchanged", bankID)
		}
	case StatusProbing:
		if flag.Status != StatusDegraded {
			return nil, fmt.Errorf("bank %s must be %s to start probing, it is %s", bankID, StatusDegraded, flag.Status)
		}
		if elapsed := now - flag.Since; elapsed < int64(cfg.CooldownSeconds) {
			return nil, fmt.Errorf("cool-down not over for bank %s: %ds of %ds", bankID, elapsed, cfg.CooldownSeconds)
		}
	case StatusHealthy:
		if flag.Status != StatusProbing {
			return nil, fmt.Errorf("bank %s must be %s before it can return to %s, it is %s", bankID, StatusProbing, StatusHealthy, flag.Status)
		}
		for _, e := range []struct {
			who string
			obs Observation
		}{{"switch", swObs}, {"network operator", opObs}} {
			if e.obs.RecordedAt <= flag.Since {
				return nil, fmt.Errorf("%s observation for bank %s predates probing", e.who, bankID)
			}
			if e.obs.TxCount < cfg.ProbeMinSample {
				return nil, fmt.Errorf("%s has seen only %d probe payments for bank %s, need %d", e.who, e.obs.TxCount, bankID, cfg.ProbeMinSample)
			}
			if classify(e.obs, cfg.ProbeMinSample, cfg) != StatusHealthy {
				return nil, fmt.Errorf("%s probe payments to bank %s are still failing", e.who, bankID)
			}
		}
	default:
		return nil, fmt.Errorf("status must be %s, %s or %s", StatusDegraded, StatusProbing, StatusHealthy)
	}

	next := BankFlag{
		BankID: bankID, Status: status, Since: now, Seq: flag.Seq + 1, ProposedBy: msp, Reason: reason,
		Mode: bank.Mode, HoldMs: bank.HoldMs, ProbeShareBps: cfg.ProbeShareBps,
		SwitchObs: swObs, OperatorObs: opObs, TxID: ctx.GetStub().GetTxID(),
	}
	if err := c.writeFlag(ctx, &next); err != nil {
		return nil, err
	}
	return &next, nil
}

// ---- queries ----------------------------------------------------------------

// GetBankFlag returns one bank's current flag.
func (c *HealthContract) GetBankFlag(ctx contractapi.TransactionContextInterface, bankID string) (*BankFlag, error) {
	var flag BankFlag
	ok, err := getJSON(ctx, prefixFlag+bankID, &flag)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("bank %s has no flag", bankID)
	}
	return &flag, nil
}

// GetAllFlags returns every bank's current flag. Switches call it once at
// start-up to fill their cache, then follow BankFlagChanged events.
func (c *HealthContract) GetAllFlags(ctx contractapi.TransactionContextInterface) ([]*BankFlag, error) {
	flags := []*BankFlag{}
	err := c.scan(ctx, prefixFlag, func(raw []byte) error {
		var f BankFlag
		if err := json.Unmarshal(raw, &f); err != nil {
			return err
		}
		flags = append(flags, &f)
		return nil
	})
	return flags, err
}

// GetFlagHistory returns every flag a bank has had, oldest first.
func (c *HealthContract) GetFlagHistory(ctx contractapi.TransactionContextInterface, bankID string) ([]*BankFlag, error) {
	flags := []*BankFlag{}
	err := c.scan(ctx, prefixLog+bankID+":", func(raw []byte) error {
		var f BankFlag
		if err := json.Unmarshal(raw, &f); err != nil {
			return err
		}
		flags = append(flags, &f)
		return nil
	})
	return flags, err
}

// GetBank returns a registered bank.
func (c *HealthContract) GetBank(ctx contractapi.TransactionContextInterface, bankID string) (*Bank, error) {
	return c.loadBank(ctx, bankID)
}

// GetObservation returns the latest observation an org recorded for a bank.
func (c *HealthContract) GetObservation(ctx contractapi.TransactionContextInterface, bankID string, observerMSP string) (*Observation, error) {
	var o Observation
	ok, err := getJSON(ctx, prefixObs+bankID+":"+observerMSP, &o)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("no observation by %s for bank %s", observerMSP, bankID)
	}
	return &o, nil
}

// scan visits every value whose key starts with prefix, in key order.
func (c *HealthContract) scan(ctx contractapi.TransactionContextInterface, prefix string, visit func([]byte) error) error {
	// Every prefix ends in ':', so the range ends just before the same prefix ending in ';'.
	end := prefix[:len(prefix)-1] + string(prefix[len(prefix)-1]+1)
	it, err := ctx.GetStub().GetStateByRange(prefix, end)
	if err != nil {
		return fmt.Errorf("range read failed: %v", err)
	}
	defer it.Close()
	for it.HasNext() {
		kv, err := it.Next()
		if err != nil {
			return err
		}
		if err := visit(kv.Value); err != nil {
			return err
		}
	}
	return nil
}
