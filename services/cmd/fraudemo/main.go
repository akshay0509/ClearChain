// Command fraudemo walks through a fraud recovery on the live network:
//
//	Two customers, at Bank A and the Sponsor Bank, are defrauded into paying a
//	mule account at Bank B, which moves part of the money on to Bank A.
//
// Each bank and the investigating authority act with their own identity, and
// every write is endorsed by exactly the Investigator and the bank concerned.
// Account balances and liens live in the bank simulator, standing in for the
// banks' core systems.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"clearchain/services/internal/fabric"

	"github.com/hyperledger/fabric-gateway/pkg/client"
)

const (
	channel   = "mychannel"
	chaincode = "fraudhold"
	invMSP    = "InvestigatorOrgMSP"
)

var bankMSP = map[string]string{"BANKA": "BankAOrgMSP", "BANKB": "BankBOrgMSP", "SPONSORBANK": "SponsorBankOrgMSP"}

type actor struct {
	name string
	cl   *fabric.Client
	c    *client.Contract
}

var banksURL string

func say(format string, a ...any) { fmt.Printf("\n== "+format+"\n", a...) }

func rupees(paise int64) string {
	neg := paise < 0
	if neg {
		paise = -paise
	}
	s := strconv.FormatInt(paise/100, 10)
	if len(s) > 3 {
		head, tail := s[:len(s)-3], s[len(s)-3:]
		var parts []string
		for len(head) > 2 {
			parts = append([]string{head[len(head)-2:]}, parts...)
			head = head[:len(head)-2]
		}
		parts = append([]string{head}, parts...)
		s = strings.Join(parts, ",") + "," + tail
	}
	if neg {
		return "-Rs " + s
	}
	return "Rs " + s
}

func hash(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}

// transient reports errors that come from one peer being a block behind
// another, which clear on their own.
func transient(err error) bool {
	return fabric.Conflict(err) || fabric.EndorsersDisagreed(err) || fabric.Mentions(err, "does not exist") ||
		// The first use of a contract on an org: Drunix creates its table in
		// the org's database, and a lite peer and committing peer can race to do it.
		fabric.Mentions(err, "Restarting a DDL transaction")
}

// submit sends a transaction endorsed by exactly the given orgs, optionally
// with case details as transient data, and waits for it to commit.
func (a *actor) submit(endorsers []string, details string, fn string, args ...string) ([]byte, error) {
	opts := []client.ProposalOption{client.WithArguments(args...), client.WithEndorsingOrganizations(endorsers...)}
	if details != "" {
		opts = append(opts, client.WithTransient(map[string][]byte{"details": []byte(details)}))
	}
	var last error
	for attempt := 0; attempt < 20; attempt++ {
		res, commit, err := a.c.SubmitAsync(fn, opts...)
		if err == nil {
			st, serr := commit.Status()
			if serr != nil {
				err = serr
			} else if !st.Successful {
				err = fmt.Errorf("transaction failed validation: %s", st.Code)
				if st.Code.String() == "MVCC_READ_CONFLICT" {
					last = err
					time.Sleep(250 * time.Millisecond)
					continue
				}
				return nil, err
			} else {
				return res, nil
			}
		}
		if !transient(err) {
			return nil, err
		}
		last = err
		time.Sleep(250 * time.Millisecond)
	}
	return nil, last
}

// query evaluates a read, retrying briefly while this org's peer catches up.
func (a *actor) query(fn string, args ...string) ([]byte, error) {
	var last error
	for attempt := 0; attempt < 20; attempt++ {
		out, err := a.c.EvaluateTransaction(fn, args...)
		if err == nil || !transient(err) {
			return out, err
		}
		last = err
		time.Sleep(250 * time.Millisecond)
	}
	return nil, last
}

// reason pulls the chaincode's own message out of a gateway error.
func reason(err error) string {
	r := fabric.Describe(err)
	if i := strings.LastIndex(r, "message:\""); i >= 0 {
		r = strings.TrimSuffix(strings.TrimSpace(r[i+9:]), "\"")
	}
	r = strings.TrimPrefix(r, "chaincode response 500, ")
	if len(r) > 220 {
		r = r[:220] + "..."
	}
	return r
}

// refused checks that an action is rejected with the expected reason.
func refused(what, want string, err error) error {
	if err == nil {
		return fmt.Errorf("%s should have been refused but was accepted", what)
	}
	if !strings.Contains(fabric.Describe(err), want) {
		return fmt.Errorf("%s was refused, but not for the expected reason (%q): %s", what, want, fabric.Describe(err))
	}
	fmt.Printf("   refused, as it should be: %s\n     -> %s\n", what, reason(err))
	return nil
}

// bank calls the bank simulator and decodes the account it returns.
func bank(path string, body map[string]any) (map[string]any, int, error) {
	raw, _ := json.Marshal(body)
	resp, err := http.Post(banksURL+path, "application/json", bytes.NewReader(raw))
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var out map[string]any
	json.Unmarshal(data, &out)
	if out == nil {
		out = map[string]any{"error": strings.TrimSpace(string(data))}
	}
	return out, resp.StatusCode, nil
}

func account(bankID, h string) (map[string]any, error) {
	resp, err := http.Get(banksURL + "/bank/" + bankID + "/account/" + h)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out map[string]any
	return out, json.NewDecoder(resp.Body).Decode(&out)
}

func paise(m map[string]any, k string) int64 {
	if v, ok := m[k].(float64); ok {
		return int64(v)
	}
	return 0
}

func showAccount(label, bankID, h string) (map[string]any, error) {
	a, err := account(bankID, h)
	if err != nil {
		return nil, err
	}
	fmt.Printf("   %-30s balance %12s   held %12s   usable %12s\n", label, rupees(paise(a, "balance")), rupees(paise(a, "liened")), rupees(paise(a, "usable")))
	return a, nil
}

func main() {
	flag.StringVar(&banksURL, "banks", "http://localhost:9101", "bank systems base URL")
	flag.Parse()
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "\nFAILED:", fabric.Describe(err))
		os.Exit(1)
	}
}

func run() error {
	connect := func(name, org string) (*actor, error) {
		cl, err := fabric.Connect(org, "User1", channel)
		if err != nil {
			return nil, err
		}
		return &actor{name: name, cl: cl, c: cl.Contract(chaincode)}, nil
	}
	inv, err := connect("Investigator", "investigator")
	if err != nil {
		return err
	}
	defer inv.cl.Close()
	bA, err := connect("Bank A", "banka")
	if err != nil {
		return err
	}
	defer bA.cl.Close()
	bB, err := connect("Bank B", "bankb")
	if err != nil {
		return err
	}
	defer bB.cl.Close()
	sp, err := connect("Sponsor Bank", "sponsorbank")
	if err != nil {
		return err
	}
	defer sp.cl.Close()
	with := func(bankID string) []string { return []string{invMSP, bankMSP[bankID]} }

	run := strconv.FormatInt(time.Now().Unix(), 10)
	victim1, victim2 := hash("victim1-"+run), hash("victim2-"+run)
	mule, second, shop := hash("mule-"+run), hash("second-"+run), hash("shop-"+run)
	rrnV1, rrnV2, rrnOn := "RRN-V1-"+run, "RRN-V2-"+run, "RRN-ON-"+run
	cmp1, cmp2, cmp3 := "CMP-"+run+"-1", "CMP-"+run+"-2", "CMP-"+run+"-3"
	case1, case2, case3 := "NCRP-"+run+"-01", "NCRP-"+run+"-02", "NCRP-"+run+"-03"

	say("The investigating authority registers the banks and the private channel it shares with each")
	for _, b := range [][2]string{{"BANKA", "caseBankA"}, {"BANKB", "caseBankB"}, {"SPONSORBANK", "caseSponsorBank"}} {
		_, err := inv.submit(with(b[0]), "", "RegisterBank", b[0], bankMSP[b[0]], b[1])
		switch {
		case err == nil:
			fmt.Printf("   registered %-12s case details shared only with the Investigator in %s\n", b[0], b[1])
		case fabric.Mentions(err, "already registered"):
			fmt.Printf("   present    %s\n", b[0])
		default:
			return fmt.Errorf("register %s: %w", b[0], err)
		}
	}

	say("The fraud: two customers are tricked into paying one mule account at Bank B")
	for _, a := range []struct {
		bank, h string
		bal     int64
	}{{"BANKA", victim1, 10_000_000}, {"SPONSORBANK", victim2, 10_000_000}, {"BANKB", mule, 2_500_000}, {"BANKA", second, 0}} {
		if _, _, err := bank("/bank/"+a.bank+"/account", map[string]any{"accountHash": a.h, "amount": a.bal}); err != nil {
			return err
		}
	}
	for _, t := range []struct {
		fromBank, from, toBank, to string
		amount                     int64
		label                      string
	}{
		{"BANKA", victim1, "BANKB", mule, 6_000_000, "victim 1 (Bank A) pays the mule " + rupees(6_000_000) + "        " + rrnV1},
		{"SPONSORBANK", victim2, "BANKB", mule, 4_000_000, "victim 2 (Sponsor Bank) pays the mule " + rupees(4_000_000) + "  " + rrnV2},
		{"BANKB", mule, "BANKA", second, 3_000_000, "the mule moves " + rupees(3_000_000) + " on to a second account at Bank A  " + rrnOn},
	} {
		if _, code, err := bank("/bank/"+t.fromBank+"/transfer", map[string]any{"accountHash": t.from, "toBank": t.toBank, "toAccountHash": t.to, "amount": t.amount}); err != nil || code != 200 {
			return fmt.Errorf("transfer failed: %v %d", err, code)
		}
		fmt.Println("   " + t.label)
	}
	fmt.Println("   the mule account also holds " + rupees(2_500_000) + " of the holder's own money")
	if _, err := showAccount("mule account at Bank B", "BANKB", mule); err != nil {
		return err
	}

	say("Each victim's bank files a complaint; the victim's details go only to the Investigator")
	if _, err := bA.submit(with("BANKA"), `{"victim":"Arun Kumar","phone":"98xxxxxx01","account":"how the call went..."}`,
		"FileComplaint", cmp1, "BANKA", victim1, "6000000", `["`+rrnV1+`"]`, case1); err != nil {
		return fmt.Errorf("complaint 1: %w", err)
	}
	fmt.Printf("   %s  filed by Bank A          lost %s   case %s\n", cmp1, rupees(6_000_000), case1)
	if _, err := sp.submit(with("SPONSORBANK"), `{"victim":"Sunita Devi","phone":"98xxxxxx02"}`,
		"FileComplaint", cmp2, "SPONSORBANK", victim2, "4000000", `["`+rrnV2+`"]`, case2); err != nil {
		return fmt.Errorf("complaint 2: %w", err)
	}
	fmt.Printf("   %s  filed by Sponsor Bank    lost %s   case %s\n", cmp2, rupees(4_000_000), case2)

	say("The receiving banks record where the money went")
	for _, t := range []struct {
		who                                     *actor
		rrn, parent, fromBank, from, toBank, to string
		amount                                  int64
	}{
		{bB, rrnV1, "", "BANKA", victim1, "BANKB", mule, 6_000_000},
		{bB, rrnV2, "", "SPONSORBANK", victim2, "BANKB", mule, 4_000_000},
		{bA, rrnOn, rrnV1, "BANKB", mule, "BANKA", second, 3_000_000},
	} {
		if _, err := t.who.submit(with(t.toBank), "", "RecordTransfer", t.rrn, t.parent, t.fromBank, t.from, t.toBank, t.to, strconv.FormatInt(t.amount, 10)); err != nil {
			return fmt.Errorf("record %s: %w", t.rrn, err)
		}
		fmt.Printf("   %-6s recorded %s into its own customer's account\n", t.who.name, t.rrn)
	}
	if err := trace(inv, cmp1); err != nil {
		return err
	}

	say("Holds that are refused")
	place := func(who *actor, endorsers []string, holdID, cmp, rrn string, amount int64, provision, caseRef string) error {
		_, err := who.submit(endorsers, "", "PlaceHold", holdID, cmp, rrn, strconv.FormatInt(amount, 10), provision, caseRef)
		return err
	}
	if err := refused("a hold with no legal provision", "no legal provision cited",
		place(inv, with("BANKB"), "HOLD-"+run+"-X", cmp1, rrnV1, 3_000_000, "", case1)); err != nil {
		return err
	}
	if err := refused("a hold with no case reference", "no case reference given",
		place(inv, with("BANKB"), "HOLD-"+run+"-X", cmp1, rrnV1, 3_000_000, "BNSS-106", "")); err != nil {
		return err
	}
	if err := refused("freezing all "+rupees(6_000_000)+" victim 1 paid in, when half has moved on", "is still in that account",
		place(inv, with("BANKB"), "HOLD-"+run+"-X", cmp1, rrnV1, 6_000_000, "BNSS-106", case1)); err != nil {
		return err
	}
	if err := refused("a hold at Bank B endorsed by Bank A's peer instead of Bank B's", "must be endorsed by InvestigatorOrgMSP and BankBOrgMSP",
		place(inv, with("BANKA"), "HOLD-"+run+"-X", cmp1, rrnV1, 3_000_000, "BNSS-106", case1)); err != nil {
		return err
	}
	if err := refused("a hold endorsed by the Investigator alone, without the holding bank", "ENDORSEMENT_POLICY_FAILURE",
		place(inv, []string{invMSP}, "HOLD-"+run+"-X", cmp1, rrnV1, 3_000_000, "BNSS-106", case1)); err != nil {
		return err
	}

	say("Targeted holds on exactly the disputed amounts, wherever the money now sits")
	holds := []struct {
		id, cmp, rrn, bankID, acct, provision, caseRef string
		amount                                         int64
	}{
		{"HOLD-" + run + "-1", cmp1, rrnV1, "BANKB", mule, "BNSS-106", case1, 3_000_000},
		{"HOLD-" + run + "-2", cmp2, rrnV2, "BANKB", mule, "BNSS-106", case2, 4_000_000},
		{"HOLD-" + run + "-3", cmp1, rrnOn, "BANKA", second, "BNSS-106", case1, 3_000_000},
	}
	for _, h := range holds {
		if err := place(inv, with(h.bankID), h.id, h.cmp, h.rrn, h.amount, h.provision, h.caseRef); err != nil {
			return fmt.Errorf("hold %s: %w", h.id, err)
		}
		// The holding bank's core system marks a lien for just that amount.
		if _, _, err := bank("/bank/"+h.bankID+"/lien", map[string]any{"accountHash": h.acct, "holdId": h.id, "amount": h.amount}); err != nil {
			return err
		}
		fmt.Printf("   %s  %s at %-6s for %s (%s, %s)\n", h.id, rupees(h.amount), h.bankID, h.cmp, h.provision, h.caseRef)
	}
	mAcct, err := showAccount("mule account at Bank B", "BANKB", mule)
	if err != nil {
		return err
	}
	if _, err := showAccount("second account at Bank A", "BANKA", second); err != nil {
		return err
	}
	fmt.Printf("   a whole-account freeze would block all %s; the targeted holds block %s and leave %s usable\n",
		rupees(paise(mAcct, "balance")), rupees(paise(mAcct, "liened")), rupees(paise(mAcct, "usable")))

	say("The mule account holder spends money that is not under hold")
	if _, code, err := bank("/bank/BANKB/transfer", map[string]any{"accountHash": mule, "toBank": "BANKA", "toAccountHash": shop, "amount": 2_000_000}); err != nil || code != 200 {
		return fmt.Errorf("a payment within the usable balance should go through: %v %d", err, code)
	}
	fmt.Println("   pays " + rupees(2_000_000) + " to a shop: allowed")
	out, code, err := bank("/bank/BANKB/transfer", map[string]any{"accountHash": mule, "toBank": "BANKA", "toAccountHash": shop, "amount": 1_000_000})
	if err != nil {
		return err
	}
	if code != http.StatusForbidden {
		return fmt.Errorf("a payment into held money should be refused, got %d", code)
	}
	fmt.Printf("   tries to pay %s more: refused by Bank B (%s)\n", rupees(1_000_000), out["error"])

	say("A second, independent complaint about the same money")
	raw, err := bA.submit(with("BANKA"), `{"victim":"Arun Kumar","channel":"1930 helpline"}`,
		"FileComplaint", cmp3, "BANKA", victim1, "6000000", `["`+rrnV1+`"]`, case3)
	if err != nil {
		return fmt.Errorf("complaint 3: %w", err)
	}
	var c3 struct {
		OverlapsWith []string `json:"overlapsWith"`
	}
	json.Unmarshal(raw, &c3)
	fmt.Printf("   %s filed via the helpline, citing %s; flagged as overlapping: %v\n", cmp3, rrnV1, c3.OverlapsWith)
	if len(c3.OverlapsWith) == 0 {
		return errors.New("the second complaint was not flagged as overlapping the first")
	}
	if err := refused("a second hold on the same funds under the new complaint", "duplicate hold refused",
		place(inv, with("BANKB"), "HOLD-"+run+"-4", cmp3, rrnV1, 3_000_000, "BNSS-106", case3)); err != nil {
		return err
	}

	say("Case details stay with the Investigator and the victim's bank")
	for _, r := range []*actor{inv, bA} {
		d, err := r.query("GetCaseDetails", "complaint", cmp1)
		if err != nil {
			return fmt.Errorf("%s reading case details: %w", r.name, err)
		}
		fmt.Printf("   %-12s reads %s: %s\n", r.name, cmp1, d)
	}
	_, err = bB.query("GetCaseDetails", "complaint", cmp1)
	if err := refused("Bank B, which holds the mule account, reading victim 1's details", "not available to this org", err); err != nil {
		return err
	}

	say("Court order: " + rupees(5_000_000) + " is recovered from the mule account and shared by what each victim lost")
	ordID := "ORD-" + run + "-1"
	raw, err = inv.submit(with("BANKB"), "", "OrderDisbursal", ordID, "BANKB", mule, "5000000", "CRT-"+run+"-77")
	if err != nil {
		return fmt.Errorf("order: %w", err)
	}
	var ord struct {
		TotalHeld         int64 `json:"totalHeld"`
		Recovered         int64 `json:"recovered"`
		ReleasedToAccount int64 `json:"releasedToAccount"`
		Shares            []struct {
			ComplaintID string `json:"complaintId"`
			VictimBank  string `json:"victimBank"`
			AmountLost  int64  `json:"amountLost"`
			HeldHere    int64  `json:"heldHere"`
			Amount      int64  `json:"amount"`
		} `json:"shares"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(raw, &ord); err != nil {
		return err
	}
	fmt.Printf("   held %s, recovered %s, %s released back to the account holder\n", rupees(ord.TotalHeld), rupees(ord.Recovered), rupees(ord.ReleasedToAccount))
	fmt.Printf("   %-22s %-12s %12s %12s %12s\n", "complaint", "victim bank", "lost", "held here", "share")
	var sum int64
	for _, s := range ord.Shares {
		fmt.Printf("   %-22s %-12s %12s %12s %12s\n", s.ComplaintID, s.VictimBank, rupees(s.AmountLost), rupees(s.HeldHere), rupees(s.Amount))
		sum += s.Amount
	}
	if sum != ord.Recovered || len(ord.Shares) != 2 || ord.Shares[0].Amount != 3_000_000 || ord.Shares[1].Amount != 2_000_000 {
		return fmt.Errorf("unexpected split: %s", raw)
	}
	if _, code, err := bank("/bank/BANKB/recover", map[string]any{"accountHash": mule, "amount": ord.Recovered}); err != nil || code != 200 {
		return fmt.Errorf("Bank B could not carry out the recovery: %v %d", err, code)
	}
	victimAcct := map[string]string{cmp1: victim1, cmp2: victim2}
	repayer := map[string]*actor{"BANKA": bA, "SPONSORBANK": sp}
	for _, s := range ord.Shares {
		if _, _, err := bank("/bank/"+s.VictimBank+"/deposit", map[string]any{"accountHash": victimAcct[s.ComplaintID], "amount": s.Amount}); err != nil {
			return err
		}
		if _, err := repayer[s.VictimBank].submit(with("BANKB"), "", "ConfirmRepayment", ordID, s.ComplaintID); err != nil {
			return fmt.Errorf("confirm repayment %s: %w", s.ComplaintID, err)
		}
		fmt.Printf("   %s credited its customer %s and confirmed it on the ledger\n", s.VictimBank, rupees(s.Amount))
	}

	say("A second order recovers what moved on to Bank A")
	ord2 := "ORD-" + run + "-2"
	if _, err := inv.submit(with("BANKA"), "", "OrderDisbursal", ord2, "BANKA", second, "3000000", "CRT-"+run+"-78"); err != nil {
		return fmt.Errorf("order 2: %w", err)
	}
	if _, _, err := bank("/bank/BANKA/recover", map[string]any{"accountHash": second, "amount": 3_000_000}); err != nil {
		return err
	}
	if _, _, err := bank("/bank/BANKA/deposit", map[string]any{"accountHash": victim1, "amount": 3_000_000}); err != nil {
		return err
	}
	if _, err := bA.submit(with("BANKA"), "", "ConfirmRepayment", ord2, cmp1); err != nil {
		return fmt.Errorf("confirm repayment 2: %w", err)
	}
	fmt.Println("   " + rupees(3_000_000) + " recovered from the second account and repaid to victim 1")

	say("Where everyone ends up")
	for _, c := range []string{cmp1, cmp2} {
		raw, err := inv.query("GetComplaint", c)
		if err != nil {
			return err
		}
		var comp struct {
			AmountLost int64 `json:"amountLost"`
			Recovered  int64 `json:"recovered"`
			Held       int64 `json:"held"`
		}
		json.Unmarshal(raw, &comp)
		fmt.Printf("   %s  lost %s, recovered %s, still held %s\n", c, rupees(comp.AmountLost), rupees(comp.Recovered), rupees(comp.Held))
	}
	for _, o := range []string{ordID, ord2} {
		raw, err := inv.query("GetOrder", o)
		if err != nil {
			return err
		}
		var st struct {
			Status string `json:"status"`
		}
		json.Unmarshal(raw, &st)
		if st.Status != "COMPLETE" {
			return fmt.Errorf("order %s is %s, not COMPLETE", o, st.Status)
		}
		fmt.Printf("   %s  %s\n", o, st.Status)
	}
	if _, err := showAccount("victim 1 at Bank A", "BANKA", victim1); err != nil {
		return err
	}
	if _, err := showAccount("victim 2 at Sponsor Bank", "SPONSORBANK", victim2); err != nil {
		return err
	}
	if _, err := showAccount("mule account at Bank B", "BANKB", mule); err != nil {
		return err
	}
	fmt.Println("\nRUN=" + run)
	fmt.Println("Fraud-recovery demo complete.")
	return nil
}

// trace prints how a complaint's money moved and what can still be held.
func trace(inv *actor, complaintID string) error {
	raw, err := inv.query("GetTrace", complaintID)
	if err != nil {
		return fmt.Errorf("trace: %w", err)
	}
	var rows []struct {
		Depth           int    `json:"depth"`
		RRN             string `json:"rrn"`
		FromBank        string `json:"fromBank"`
		ToBank          string `json:"toBank"`
		Tainted         int64  `json:"tainted"`
		MovedOn         int64  `json:"movedOn"`
		AvailableToHold int64  `json:"availableToHold"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return err
	}
	fmt.Printf("   trace of %s:\n", complaintID)
	for _, r := range rows {
		fmt.Printf("   %s%-22s %-11s -> %-6s victim money %10s  moved on %10s  can be held here %10s\n",
			strings.Repeat("  ", r.Depth-1), r.RRN, r.FromBank, r.ToBank, rupees(r.Tainted), rupees(r.MovedOn), rupees(r.AvailableToHold))
	}
	return nil
}
