// Command bankadapter connects the banks' own systems to the ledger. In a real
// deployment each bank runs its own; here one process plays that part for all
// three, using each bank org's own identity for that bank's actions.
//
// For every bank: when its core system applies a credit, the adapter confirms
// that credit on the settlement chaincode, as the payee bank's org.
//
// For the sponsor bank, which holds the aggregator's escrow and float, it also:
//   - pays the merchant from the float as soon as a payout is committed,
//   - funds the float, and
//   - runs the settlement cycle that repays the float from escrow.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"clearchain/services/internal/fabric"

	"github.com/hyperledger/fabric-gateway/pkg/client"
)

const chaincode = "settlement"

// bankOrg says which org speaks for each bank.
var bankOrg = map[string]string{"BANKA": "banka", "BANKB": "bankb", "SPONSORBANK": "sponsorbank"}

type credit struct {
	Bank   string `json:"bank"`
	RRN    string `json:"rrn"`
	Amount int64  `json:"amount"`
	at     time.Time
}

type adapter struct {
	banks     string
	contracts map[string]*client.Contract // by bank

	credits chan credit

	confirmed   atomic.Int64
	confirmFail atomic.Int64
	confirmDup  atomic.Int64
	paidOut     atomic.Int64

	mu         sync.Mutex
	creditAt   map[string]time.Time // sponsor-bank credits awaiting payout, by RRN
	toPayMs    []float64            // escrow credit applied -> merchant paid, milliseconds
	unsettled  []string             // RRNs paid from the float and not yet reimbursed
	lastCycles []json.RawMessage
}

// confirmWorker tells the ledger a credit has landed. The switch records the
// payment on its own schedule, so the record may not be there yet: wait for it.
func (a *adapter) confirmWorker() {
	for c := range a.credits {
		contract := a.contracts[c.Bank]
		var err error
		for attempt := 0; attempt < 120; attempt++ {
			_, _, err = fabric.SubmitRetry(contract, 5, "ConfirmCredit", c.RRN)
			if err == nil || !fabric.Mentions(err, "is not recorded") {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		switch {
		case err == nil:
			a.confirmed.Add(1)
		case fabric.Mentions(err, "duplicate"):
			// The core system reported the same credit twice; the ledger already has it.
			a.confirmDup.Add(1)
		default:
			a.confirmFail.Add(1)
			log.Printf("bankadapter: %s could not confirm %s: %s", c.Bank, c.RRN, fabric.Describe(err))
		}
	}
}

// payouts follows committed merchant payouts and moves the money: the sponsor
// bank credits the merchant from the float account it holds.
func (a *adapter) payouts(ctx context.Context, channel string) error {
	events, closeFn, err := fabric.Events(ctx, "sponsorbank", channel, chaincode, false)
	if err != nil {
		return err
	}
	go func() {
		defer closeFn()
		for ev := range events {
			if ev.EventName != "MerchantPaidOut" {
				continue
			}
			var p struct {
				RRN            string `json:"rrn"`
				MerchantID     string `json:"merchantId"`
				MerchantAmount int64  `json:"merchantAmount"`
			}
			if json.Unmarshal(ev.Payload, &p) != nil {
				continue
			}
			go func() {
				body, _ := json.Marshal(map[string]any{"rrn": p.RRN, "amount": p.MerchantAmount})
				resp, err := http.Post(a.banks+"/bank/SPONSORBANK/payout", "application/json", bytes.NewReader(body))
				if err != nil {
					log.Printf("bankadapter: float payout for %s failed: %v", p.RRN, err)
					return
				}
				resp.Body.Close()
				done := time.Now()
				a.paidOut.Add(1)
				a.mu.Lock()
				if t, ok := a.creditAt[p.RRN]; ok {
					a.toPayMs = append(a.toPayMs, float64(done.Sub(t).Microseconds())/1000)
					delete(a.creditAt, p.RRN)
				}
				a.unsettled = append(a.unsettled, p.RRN)
				a.mu.Unlock()
			}()
		}
	}()
	return nil
}

// runCycle repays the float for everything paid out since the last cycle.
func (a *adapter) runCycle(cycleID string) ([]json.RawMessage, error) {
	a.mu.Lock()
	rrns := a.unsettled
	a.unsettled = nil
	a.mu.Unlock()
	var out []json.RawMessage
	for i := 0; len(rrns) > 0; i++ {
		n := min(len(rrns), 100)
		batch, _ := json.Marshal(rrns[:n])
		res, _, err := fabric.SubmitRetry(a.contracts["SPONSORBANK"], 8, "ReimburseFloat", fmt.Sprintf("%s-%d", cycleID, i+1), string(batch))
		if err != nil {
			a.mu.Lock()
			a.unsettled = append(rrns, a.unsettled...)
			a.mu.Unlock()
			return out, err
		}
		out = append(out, res)
		rrns = rrns[n:]
	}
	a.mu.Lock()
	a.lastCycles = out
	a.mu.Unlock()
	return out, nil
}

func main() {
	addr := flag.String("addr", ":9103", "listen address")
	banks := flag.String("banks", "http://localhost:9101", "bank systems base URL")
	channel := flag.String("channel", "mychannel", "channel name")
	workers := flag.Int("workers", 32, "concurrent confirmations")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	a := &adapter{banks: *banks, contracts: map[string]*client.Contract{}, credits: make(chan credit, 200000), creditAt: map[string]time.Time{}}
	for bank, org := range bankOrg {
		cl, err := fabric.Connect(org, "User1", *channel)
		if err != nil {
			log.Fatalf("bankadapter: %v", err)
		}
		defer cl.Close()
		a.contracts[bank] = cl.Contract(chaincode)
	}
	for i := 0; i < *workers; i++ {
		go a.confirmWorker()
	}
	if err := a.payouts(ctx, *channel); err != nil {
		log.Fatalf("bankadapter: %v", err)
	}

	mux := http.NewServeMux()
	// The bank's core system reports a credit it has applied.
	mux.HandleFunc("POST /credited", func(w http.ResponseWriter, r *http.Request) {
		var c credit
		if err := json.NewDecoder(r.Body).Decode(&c); err != nil || c.RRN == "" || a.contracts[c.Bank] == nil {
			http.Error(w, "bank and rrn required", http.StatusBadRequest)
			return
		}
		c.at = time.Now()
		if c.Bank == "SPONSORBANK" {
			a.mu.Lock()
			a.creditAt[c.RRN] = c.at
			a.mu.Unlock()
		}
		a.credits <- c
		w.WriteHeader(http.StatusAccepted)
	})
	// The sponsor bank records money the aggregator has placed in the float.
	mux.HandleFunc("POST /float/fund", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Amount int64  `json:"amount"`
			Ref    string `json:"ref"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "amount and ref required", http.StatusBadRequest)
			return
		}
		res, err := a.contracts["SPONSORBANK"].SubmitTransaction("FundFloat", strconv.FormatInt(body.Amount, 10), body.Ref)
		if err != nil {
			http.Error(w, fabric.Describe(err), http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(res)
	})
	// The aggregator's normal settlement cycle: escrow funds are released and the float is repaid.
	mux.HandleFunc("POST /cycle", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			CycleID string `json:"cycleId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.CycleID == "" {
			http.Error(w, "cycleId required", http.StatusBadRequest)
			return
		}
		res, err := a.runCycle(body.CycleID)
		if err != nil {
			http.Error(w, fabric.Describe(err), http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(res)
	})
	mux.HandleFunc("GET /state", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		d := append([]float64(nil), a.toPayMs...)
		unsettled := len(a.unsettled)
		awaiting := len(a.creditAt)
		a.mu.Unlock()
		sort.Float64s(d)
		pct := func(p float64) float64 {
			if len(d) == 0 {
				return 0
			}
			return d[int(float64(len(d)-1)*p)]
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		json.NewEncoder(w).Encode(map[string]any{
			"creditsConfirmed":     a.confirmed.Load(),
			"confirmationsFailed":  a.confirmFail.Load(),
			"repeatCreditsIgnored": a.confirmDup.Load(),
			"waitingToConfirm":     len(a.credits),
			"merchantsPaid":        a.paidOut.Load(),
			"escrowAwaitingPayout": awaiting,
			"paidAwaitingCycle":    unsettled,
			// From the escrow credit landing at the sponsor bank to the merchant being paid.
			"creditToMerchantPaidMs": map[string]any{"samples": len(d), "p50": pct(0.50), "p95": pct(0.95), "max": pct(1)},
		})
	})
	srv := &http.Server{Addr: *addr, Handler: mux}
	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	log.Printf("bank adapter listening on %s for BANKA, BANKB and SPONSORBANK", *addr)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
