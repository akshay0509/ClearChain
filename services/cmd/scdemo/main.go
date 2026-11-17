// Command scdemo walks through a three-tier supply-chain payment on the live
// network, each party acting with its own identity:
//
//	Anchor Motors (buyer)      User1 of the Buyer org
//	Precision Castings, Tier-1 User2 of Bank A's org   (medium enterprise)
//	Shree Forgings, Tier-2     User2 of Bank B's org   (small enterprise)
//	Raju Tooling, Tier-3       User3 of Bank A's org   (micro enterprise)
//
// Suppliers do not run nodes: each holds a certificate issued under its bank.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
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
	chaincode = "supplychain"
	day       = 86400
)

var endorsers = client.WithEndorsingOrganizations("BuyerOrgMSP", "BankAOrgMSP", "BankBOrgMSP")

type actor struct {
	id, name string
	cl       *fabric.Client
	c        *client.Contract
}

func rupees(paise int64) string {
	if paise < 0 {
		return "(hidden)"
	}
	s := strconv.FormatInt(paise/100, 10)
	// Indian digit grouping: last three, then twos.
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
	return "Rs " + s
}

func say(format string, a ...any) { fmt.Printf("\n== "+format+"\n", a...) }

// members are one identity from each org that takes part in the chain.
var members []*actor

// settle waits until every member org's peer returns the same answer to a
// query. A commit is confirmed by the submitter's own org first; the other
// orgs apply the same block a moment later, and the next party should not act
// on a view its own peer does not have yet.
func settle(fn string, args ...string) error {
	deadline := time.Now().Add(10 * time.Second)
	for {
		var first []byte
		same := true
		for i, m := range members {
			out, err := m.c.EvaluateTransaction(fn, args...)
			if err != nil {
				same = false
				break
			}
			if i == 0 {
				first = out
			} else if !bytes.Equal(first, out) {
				same = false
				break
			}
		}
		if same {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the member orgs did not converge on %s%v within 10s", fn, args)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// submit sends a transaction, with private terms if amount is positive.
func (a *actor) submit(fn string, amount int64, invoice string, args ...string) ([]byte, *client.Commit, error) {
	opts := []client.ProposalOption{client.WithArguments(args...), endorsers}
	if amount > 0 {
		terms, _ := json.Marshal(map[string]any{"amount": amount, "invoiceRef": invoice})
		opts = append(opts, client.WithTransient(map[string][]byte{"terms": terms}))
	}
	res, commit, err := a.c.SubmitAsync(fn, opts...)
	if err != nil {
		return nil, nil, err
	}
	st, err := commit.Status()
	if err != nil {
		return nil, nil, err
	}
	if !st.Successful {
		return nil, nil, fmt.Errorf("transaction %s failed validation: %s", st.TransactionID, st.Code)
	}
	return res, commit, nil
}

// expectRefusal checks that the contract rejects an action, and prints why.
func expectRefusal(what string, err error) error {
	if err == nil {
		return fmt.Errorf("%s should have been refused but was accepted", what)
	}
	reason := fabric.Describe(err)
	if i := strings.LastIndex(reason, "message:\""); i >= 0 {
		reason = strings.TrimSuffix(strings.TrimSpace(reason[i+9:]), "\"")
	}
	if len(reason) > 230 {
		reason = reason[:230] + "..."
	}
	fmt.Printf("   refused, as it should be: %s\n     -> %s\n", what, reason)
	return nil
}

func main() {
	bankURL := flag.String("banks", "http://localhost:9101", "bank systems base URL (empty to skip crediting)")
	flag.Parse()
	if err := run(*bankURL); err != nil {
		fmt.Fprintln(os.Stderr, "\nFAILED:", fabric.Describe(err))
		os.Exit(1)
	}
}

func run(bankURL string) error {
	connect := func(id, name, org, user string) (*actor, error) {
		cl, err := fabric.Connect(org, user, channel)
		if err != nil {
			return nil, err
		}
		return &actor{id: id, name: name, cl: cl, c: cl.Contract(chaincode)}, nil
	}
	buyer, err := connect("ANCHOR", "Anchor Motors", "buyer", "User1")
	if err != nil {
		return err
	}
	defer buyer.cl.Close()
	t1, err := connect("TIER1", "Precision Castings", "banka", "User2")
	if err != nil {
		return err
	}
	defer t1.cl.Close()
	t2, err := connect("TIER2", "Shree Forgings", "bankb", "User2")
	if err != nil {
		return err
	}
	defer t2.cl.Close()
	t3, err := connect("TIER3", "Raju Tooling", "banka", "User3")
	if err != nil {
		return err
	}
	defer t3.cl.Close()
	outsider, err := connect("", "the Switch org", "switch", "User1")
	if err != nil {
		return err
	}
	defer outsider.cl.Close()
	members = []*actor{buyer, t1, t2}

	say("Each party registers itself, with its own identity")
	acct := func(id string) string {
		sum := sha256.Sum256([]byte("account-of-" + id))
		return hex.EncodeToString(sum[:])
	}
	for _, p := range []struct {
		a                        *actor
		role, udyam, class, bank string
	}{
		{buyer, "BUYER", "", "", "BANKA"},
		{t1, "SUPPLIER", "UDYAM-MH-12-0000001", "MEDIUM", "BANKA"},
		{t2, "SUPPLIER", "UDYAM-MH-12-0000002", "SMALL", "BANKB"},
		{t3, "SUPPLIER", "UDYAM-MH-12-0000003", "MICRO", "BANKA"},
	} {
		_, _, err := p.a.submit("RegisterParty", 0, "", p.a.id, p.a.name, p.role, p.udyam, p.class, p.bank, acct(p.a.id))
		switch {
		case err == nil:
			fmt.Printf("   registered %-6s %-19s %-8s %-6s %s\n", p.a.id, p.a.name, p.role, p.class, p.udyam)
		case fabric.Mentions(err, "already registered"):
			fmt.Printf("   present    %-6s %s\n", p.a.id, p.a.name)
		default:
			return fmt.Errorf("register %s: %w", p.a.id, err)
		}
		if err := settle("GetParty", p.a.id); err != nil {
			return err
		}
	}

	now := time.Now().Unix()
	accepted := now - 12*day // goods were accepted 12 days ago
	run := strconv.FormatInt(now, 10)
	chain, ob1, ob2, ob3 := "PO-"+run, "PO-"+run+"-1", "PO-"+run+"-2", "PO-"+run+"-3"
	d := func(t int64) string { return strconv.FormatInt(t, 10) }

	say("The buyer opens chain %s: it owes Tier-1 %s", chain, rupees(100_000_000))
	if err := expectRefusal("Tier-1 opening a chain as if it were the buyer",
		third(t1.submit("CreateChain", 100, "INV-X", chain, ob1, "TIER2", d(accepted), "30", d(accepted+30*day)))); err != nil {
		return err
	}
	if _, _, err := buyer.submit("CreateChain", 100_000_000, "INV-ANCHOR-2291", chain, ob1, "TIER1", d(accepted), "30", d(accepted+30*day)); err != nil {
		return fmt.Errorf("create chain: %w", err)
	}
	if err := settle("GetChain", chain); err != nil {
		return err
	}
	fmt.Printf("   %s  %s owes %s %s  (active)\n", ob1, buyer.name, t1.name, rupees(100_000_000))

	say("Tier-1 registers what it owes Tier-2; the buyer, the tier above, approves")
	if err := expectRefusal("Tier-2 registering an obligation before it has a place in the chain",
		third(t2.submit("RegisterObligation", 25_000_000, "INV-T2-41", ob3, chain, "TIER3", d(accepted), "0", d(accepted+15*day)))); err != nil {
		return err
	}
	if err := expectRefusal("the buyer registering Tier-1's obligation on Tier-1's behalf",
		third(buyer.submit("RegisterObligation", 60_000_000, "INV-T1-907", ob2, chain, "TIER2", d(accepted), "30", d(accepted+30*day)))); err != nil {
		return err
	}
	if err := expectRefusal("Tier-1 passing down more than it is owed",
		third(t1.submit("RegisterObligation", 100_000_001, "INV-T1-907", ob2, chain, "TIER2", d(accepted), "30", d(accepted+30*day)))); err != nil {
		return err
	}
	if _, _, err := t1.submit("RegisterObligation", 60_000_000, "INV-T1-907", ob2, chain, "TIER2", d(accepted), "30", d(accepted+30*day)); err != nil {
		return fmt.Errorf("tier-1 register: %w", err)
	}
	if err := settle("GetChain", chain); err != nil {
		return err
	}
	fmt.Printf("   %s  %s owes %s %s  (registered, awaiting the buyer)\n", ob2, t1.name, t2.name, rupees(60_000_000))
	if err := expectRefusal("Tier-2 approving the obligation it benefits from", third(t2.submit("ApproveObligation", 0, "", ob2))); err != nil {
		return err
	}
	if err := expectRefusal("Tier-2 passing money down before its own place is approved",
		third(t2.submit("RegisterObligation", 25_000_000, "INV-T2-41", ob3, chain, "TIER3", d(accepted), "0", d(accepted+15*day)))); err != nil {
		return err
	}
	if _, _, err := buyer.submit("ApproveObligation", 0, "", ob2); err != nil {
		return fmt.Errorf("buyer approve: %w", err)
	}
	if err := settle("GetChain", chain); err != nil {
		return err
	}
	fmt.Printf("   %s  approved by %s\n", ob2, buyer.name)

	say("Tier-2 registers what it owes Tier-3; Tier-1, the tier above, approves")
	if _, _, err := t2.submit("RegisterObligation", 25_000_000, "INV-T2-41", ob3, chain, "TIER3", d(accepted), "0", d(accepted+15*day)); err != nil {
		return fmt.Errorf("tier-2 register: %w", err)
	}
	if err := settle("GetChain", chain); err != nil {
		return err
	}
	fmt.Printf("   %s  %s owes %s %s  (registered, awaiting Tier-1)\n", ob3, t2.name, t3.name, rupees(25_000_000))
	if err := expectRefusal("the buyer approving in Tier-1's place", third(buyer.submit("ApproveObligation", 0, "", ob3))); err != nil {
		return err
	}
	if _, _, err := t1.submit("ApproveObligation", 0, "", ob3); err != nil {
		return fmt.Errorf("tier-1 approve: %w", err)
	}
	if err := settle("GetChain", chain); err != nil {
		return err
	}
	fmt.Printf("   %s  approved by %s\n", ob3, t1.name)

	say("Section 43B(h) view for this chain, today (goods accepted 12 days ago)")
	if err := deadlineView(buyer, chain, 0); err != nil {
		return err
	}
	say("The same view four days from now, if nothing is paid")
	if err := deadlineView(buyer, chain, now+4*day); err != nil {
		return err
	}

	say("Contract values are private to the orgs involved")
	raw, err := t3.c.EvaluateTransaction("GetTerms", ob3)
	if err != nil {
		return fmt.Errorf("tier-3 reading its own terms: %w", err)
	}
	fmt.Printf("   Tier-3 reads its own terms:   %s\n", raw)
	_, err = outsider.c.EvaluateTransaction("GetTerms", ob3)
	if err := expectRefusal("the Switch org, not party to this chain, reading the same terms", err); err != nil {
		return err
	}
	raw, err = outsider.c.EvaluateTransaction("GetChain", chain)
	if err != nil {
		return fmt.Errorf("outsider reading public chain: %w", err)
	}
	if strings.Contains(string(raw), "INV-") || strings.Contains(string(raw), "25000000") {
		return errors.New("the public record leaks an amount or invoice reference")
	}
	fmt.Printf("   The Switch org can still see that the obligations exist and their dates (%d bytes, no amounts or invoice references)\n", len(raw))

	say("The buyer releases one payment of %s", rupees(100_000_000))
	if err := expectRefusal("Tier-1 releasing the buyer's payment", third(t1.submit("ReleasePayment", 0, "", chain, "UTR"+run))); err != nil {
		return err
	}
	// The buyer's bank listens for the release and moves the money.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, closeEvents, err := fabric.Events(ctx, "buyer", channel, chaincode, false)
	if err != nil {
		return err
	}
	defer closeEvents()

	_, commit, err := buyer.submit("ReleasePayment", 0, "", chain, "UTR"+run)
	if err != nil {
		return fmt.Errorf("release: %w", err)
	}
	if err := settle("GetChain", chain); err != nil {
		return err
	}
	st, _ := commit.Status()
	fmt.Printf("   committed: transaction %s in block %d\n", st.TransactionID, st.BlockNumber)

	raw, err = buyer.c.EvaluateTransaction("GetRelease", chain)
	if err != nil {
		return fmt.Errorf("read release: %w", err)
	}
	var rel struct {
		Total   int64  `json:"total"`
		TxID    string `json:"txId"`
		Credits []struct {
			PartyID    string `json:"partyId"`
			Name       string `json:"name"`
			Level      int    `json:"level"`
			BankID     string `json:"bankId"`
			Received   int64  `json:"received"`
			PassedDown int64  `json:"passedDown"`
			Net        int64  `json:"net"`
		} `json:"credits"`
	}
	if err := json.Unmarshal(raw, &rel); err != nil {
		return err
	}
	fmt.Printf("   %-7s %-19s %-6s %14s %14s %14s\n", "tier", "party", "bank", "owed from above", "passed down", "credited")
	var sum int64
	for _, c := range rel.Credits {
		fmt.Printf("   Tier-%d  %-19s %-6s %14s %14s %14s\n", c.Level, c.Name, c.BankID, rupees(c.Received), rupees(c.PassedDown), rupees(c.Net))
		sum += c.Net
	}
	if sum != rel.Total || rel.TxID != st.TransactionID {
		return fmt.Errorf("split does not match the release: sum %d total %d", sum, rel.Total)
	}
	fmt.Printf("   credits add up to %s, all written by the one transaction above\n", rupees(sum))

	raw, err = buyer.c.EvaluateTransaction("GetChain", chain)
	if err != nil {
		return err
	}
	var view struct {
		Chain struct {
			Status      string `json:"status"`
			ReleaseTxID string `json:"releaseTxId"`
		} `json:"chain"`
		Obligations []struct {
			ObligationID string `json:"obligationId"`
			Status       string `json:"status"`
			PaidAt       int64  `json:"paidAt"`
			PaidOnTime   bool   `json:"paidOnTime"`
		} `json:"obligations"`
	}
	if err := json.Unmarshal(raw, &view); err != nil {
		return err
	}
	for _, o := range view.Obligations {
		if o.Status != "PAID" || o.PaidAt != view.Obligations[0].PaidAt {
			return fmt.Errorf("obligation %s is %s, not paid in the same commit", o.ObligationID, o.Status)
		}
		fmt.Printf("   %s  PAID at %s, within the statutory period: %v\n", o.ObligationID, time.Unix(o.PaidAt, 0).Format("15:04:05"), o.PaidOnTime)
	}

	if bankURL != "" {
		say("The buyer's bank sees the release event and credits each supplier's bank")
		select {
		case ev := <-events:
			fmt.Printf("   event %s from block %d: %s\n", ev.EventName, ev.BlockNumber, ev.Payload)
		case <-time.After(15 * time.Second):
			return errors.New("no release event within 15s")
		}
		for _, c := range rel.Credits {
			ref := "UTR" + run + "-" + c.PartyID
			body, _ := json.Marshal(map[string]any{"rrn": ref, "amount": c.Net})
			resp, err := http.Post(bankURL+"/bank/"+c.BankID+"/credit", "application/json", bytes.NewReader(body))
			if err != nil {
				return fmt.Errorf("credit %s: %w", c.PartyID, err)
			}
			resp.Body.Close()
			check, err := http.Get(bankURL + "/bank/" + c.BankID + "/payment/" + ref)
			if err != nil {
				return err
			}
			var rec struct {
				Credited bool  `json:"credited"`
				Amount   int64 `json:"amount"`
			}
			json.NewDecoder(check.Body).Decode(&rec)
			check.Body.Close()
			fmt.Printf("   %s at %-6s credited=%v %s  (ref %s)\n", c.Name, c.BankID, rec.Credited, rupees(rec.Amount), ref)
		}
	}

	say("A second release of the same chain")
	if err := expectRefusal("the buyer releasing the same chain again", third(buyer.submit("ReleasePayment", 0, "", chain, "UTR"+run+"B"))); err != nil {
		return err
	}
	if err := deadlineView(buyer, chain, 0); err != nil {
		return err
	}
	fmt.Println("\nSupply-chain demo complete.")
	return nil
}

func third(_ []byte, _ *client.Commit, err error) error { return err }

// deadlineView prints the Section 43B(h) view for one chain.
func deadlineView(a *actor, chain string, asOf int64) error {
	raw, err := a.c.EvaluateTransaction("DeadlineView", strconv.FormatInt(asOf, 10), "3")
	if err != nil {
		return fmt.Errorf("deadline view: %w", err)
	}
	var rows []struct {
		ObligationID    string `json:"obligationId"`
		ChainID         string `json:"chainId"`
		DebtorID        string `json:"debtorId"`
		CreditorID      string `json:"creditorId"`
		CreditorClass   string `json:"creditorClass"`
		DaysRemaining   int64  `json:"daysRemaining"`
		Flag            string `json:"flag"`
		DeductionAtRisk bool   `json:"deductionAtRisk"`
		Amount          int64  `json:"amount"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return err
	}
	n := 0
	for _, r := range rows {
		if r.ChainID != chain {
			continue
		}
		n++
		note := ""
		switch r.Flag {
		case "DUE_SOON":
			note = fmt.Sprintf("%d day(s) left; %s must pay or lose the deduction", r.DaysRemaining, r.DebtorID)
		case "OVERDUE":
			note = fmt.Sprintf("%d day(s) overdue; %s's deduction is disallowed until paid", -r.DaysRemaining, r.DebtorID)
		case "OK":
			note = fmt.Sprintf("%d day(s) left", r.DaysRemaining)
		case "NOT_COVERED":
			note = "creditor is a medium enterprise; 43B(h) does not apply"
		}
		fmt.Printf("   %-11s %s owes %s (%s) %13s  %s\n", r.Flag, r.DebtorID, r.CreditorID, r.CreditorClass, rupees(r.Amount), note)
	}
	if n == 0 {
		fmt.Println("   nothing outstanding in this chain")
	}
	return nil
}
