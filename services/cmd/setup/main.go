// Command setup prepares a freshly deployed network for the demos. Each step
// is done with the identity of the org that is allowed to do it, and every step
// is safe to run again.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"

	"clearchain/services/internal/fabric"
)

const channel = "mychannel"

var banks = [][3]string{
	{"BANKA", "Bank A", "BankAOrgMSP"},
	{"BANKB", "Bank B", "BankBOrgMSP"},
	{"SPONSORBANK", "Sponsor Bank", "SponsorBankOrgMSP"},
}

func main() {
	tat := flag.Int("tat", 60, "demo turnaround-time deadline in seconds (the real rule is one day)")
	margin := flag.Int("margin", 15, "seconds before the deadline at which auto-reversal opens")
	float := flag.Int64("float", 500_000_000, "settlement float to fund if empty, in paise")
	flag.Parse()
	if err := run(*tat, *margin, *float); err != nil {
		fmt.Fprintln(os.Stderr, "FAILED:", fabric.Describe(err))
		os.Exit(1)
	}
}

// step runs one registration and treats "already done" as success.
func step(what string, err error) error {
	switch {
	case err == nil:
		fmt.Println("  done:   ", what)
	case fabric.Mentions(err, "already registered"):
		fmt.Println("  present:", what)
	default:
		return fmt.Errorf("%s: %w", what, err)
	}
	return nil
}

func run(tat, margin int, float int64) error {
	npci, err := fabric.Connect("npci", "User1", channel)
	if err != nil {
		return err
	}
	defer npci.Close()
	aggregator, err := fabric.Connect("aggregator", "User1", channel)
	if err != nil {
		return err
	}
	defer aggregator.Close()
	sponsor, err := fabric.Connect("sponsorbank", "User1", channel)
	if err != nil {
		return err
	}
	defer sponsor.Close()

	fmt.Println("Health flags: the network operator registers the banks")
	for _, b := range banks {
		_, err := npci.Contract("healthflag").SubmitTransaction("RegisterBank", b[0], b[1], b[2])
		if err := step(b[0], err); err != nil {
			return err
		}
	}

	fmt.Println("Settlement: the network operator registers the banks and sets the deadline rule")
	for _, b := range banks {
		_, err := npci.Contract("settlement").SubmitTransaction("RegisterBank", b[0], b[2])
		if err := step(b[0], err); err != nil {
			return err
		}
	}
	rule, _ := json.Marshal(map[string]int{"tatSeconds": tat, "reverseMarginSeconds": margin})
	if _, err := npci.Contract("settlement").SubmitTransaction("SetConfig", string(rule)); err != nil {
		return fmt.Errorf("set deadline rule: %w", err)
	}
	fmt.Printf("  done:    deadline %ds after debit, auto-reversal opens %ds before it\n", tat, margin)

	fmt.Println("Settlement: the aggregator onboards its merchants (2% fee, 18% GST on the fee)")
	for _, m := range [][2]string{{"M001", "Raju Electronics"}, {"M002", "Lakshmi Kirana Store"}} {
		acct := sha256.Sum256([]byte("settlement-account-of-" + m[0]))
		_, err := aggregator.Contract("settlement").SubmitTransaction("RegisterMerchant", m[0], m[1], "200", "1800", "SPONSORBANK", hex.EncodeToString(acct[:]))
		if err := step(m[0]+" "+m[1], err); err != nil {
			return err
		}
	}

	fmt.Println("Settlement: the sponsor bank records the aggregator's pre-funded float")
	raw, err := sponsor.Contract("settlement").EvaluateTransaction("GetFloat")
	if err != nil {
		return fmt.Errorf("read float: %w", err)
	}
	var f struct {
		Total int64 `json:"total"`
	}
	json.Unmarshal(raw, &f)
	if f.Total == 0 {
		ref := "FUND-" + strconv.FormatInt(time.Now().Unix(), 10)
		if _, err := sponsor.Contract("settlement").SubmitTransaction("FundFloat", strconv.FormatInt(float, 10), ref); err != nil {
			return fmt.Errorf("fund float: %w", err)
		}
		fmt.Printf("  done:    funded Rs %.2f (%s)\n", float64(float)/100, ref)
	} else {
		fmt.Printf("  present: float holds Rs %.2f\n", float64(f.Total)/100)
	}
	fmt.Println("Setup complete.")
	return nil
}
