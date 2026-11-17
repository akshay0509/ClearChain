// Command watcher is the off-chain turnaround-time watcher. It follows the
// settlement chaincode's events to know which payments are debited but not yet
// confirmed, and when one nears its deadline it asks the chaincode to reverse it.
//
// The watcher only asks. The chaincode checks the payment's own on-chain
// timestamps and refuses a request that comes too early or for a payment that
// has since been confirmed. Once the reversal is committed, the watcher has the
// payer bank return the money.
//
// It runs with the network operator's identity.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"time"

	"clearchain/services/internal/fabric"
)

const chaincode = "settlement"

type pending struct {
	RRN        string `json:"rrn"`
	PayerBank  string `json:"payerBank"`
	Amount     int64  `json:"amount"`
	EarliestAt int64  `json:"earliestAt"`
	DeadlineAt int64  `json:"deadlineAt"`
	asking     bool
}

type config struct {
	TatSeconds           int64 `json:"tatSeconds"`
	ReverseMarginSeconds int64 `json:"reverseMarginSeconds"`
}

func main() {
	addr := flag.String("addr", ":9105", "listen address")
	channel := flag.String("channel", "mychannel", "channel name")
	operator := flag.String("operator", "http://localhost:9102", "network operator hop base URL")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	cl, err := fabric.Connect("npci", "User1", *channel)
	if err != nil {
		log.Fatalf("watcher: %v", err)
	}
	defer cl.Close()
	contract := cl.Contract(chaincode)

	var mu sync.Mutex
	open := map[string]*pending{}
	var reversals []json.RawMessage
	var refused int
	cfg := config{TatSeconds: 86400, ReverseMarginSeconds: 3600}

	loadConfig := func() {
		raw, err := contract.EvaluateTransaction("GetConfig")
		if err != nil {
			log.Printf("watcher: read config: %s", fabric.Describe(err))
			return
		}
		var c config
		if json.Unmarshal(raw, &c) == nil && c.TatSeconds > 0 {
			mu.Lock()
			cfg = c
			mu.Unlock()
		}
	}
	loadConfig()

	// Replay from the first block, so a restarted watcher rebuilds its view of
	// what is still unconfirmed from the ledger itself.
	events, closeEvents, err := fabric.Events(ctx, "npci", *channel, chaincode, true)
	if err != nil {
		log.Fatalf("watcher: %v", err)
	}
	defer closeEvents()
	go func() {
		for ev := range events {
			var p pending
			if json.Unmarshal(ev.Payload, &p) != nil || p.RRN == "" {
				continue
			}
			mu.Lock()
			switch ev.EventName {
			case "PaymentRecorded":
				open[p.RRN] = &p
			case "CreditConfirmed", "PaymentReversed":
				delete(open, p.RRN)
			}
			mu.Unlock()
		}
	}()

	reverse := func(p pending) {
		res, _, err := fabric.SubmitRetry(contract, 5, "RequestReversal", p.RRN)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			if fabric.Mentions(err, "too early") {
				// The chaincode's clock check disagreed with ours; ask again shortly.
				if cur := open[p.RRN]; cur != nil {
					cur.asking = false
				}
				return
			}
			// Confirmed or reversed in the meantime: nothing left to do.
			refused++
			delete(open, p.RRN)
			log.Printf("watcher: reversal of %s refused: %s", p.RRN, fabric.Describe(err))
			return
		}
		delete(open, p.RRN)
		reversals = append(reversals, res)
		log.Printf("watcher: %s auto-reversed on the ledger: %s", p.RRN, res)
		// The ledger says it is reversed; now the payer bank returns the money.
		go func() {
			body, _ := json.Marshal(map[string]any{"rrn": p.RRN, "amount": p.Amount})
			resp, err := http.Post(*operator+"/upi/"+p.PayerBank+"/reverse", "application/json", bytes.NewReader(body))
			if err != nil {
				log.Printf("watcher: payer bank reversal for %s failed: %v", p.RRN, err)
				return
			}
			resp.Body.Close()
		}()
	}

	go func() {
		tick := time.NewTicker(500 * time.Millisecond)
		reload := time.NewTicker(10 * time.Second)
		defer tick.Stop()
		defer reload.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-reload.C:
				loadConfig()
			case <-tick.C:
				now := time.Now().Unix()
				mu.Lock()
				for _, p := range open {
					// One second inside the window, so the chaincode's own check agrees.
					if !p.asking && now >= p.DeadlineAt-cfg.ReverseMarginSeconds+1 {
						p.asking = true
						go reverse(*p)
					}
				}
				mu.Unlock()
			}
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /state", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		list := make([]pending, 0, len(open))
		for _, p := range open {
			list = append(list, *p)
		}
		out := map[string]any{"unconfirmed": len(open), "unconfirmedPayments": list, "reversals": reversals,
			"requestsRefused": refused, "rule": cfg}
		mu.Unlock()
		if len(list) > 50 {
			out["unconfirmedPayments"] = list[:50]
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		json.NewEncoder(w).Encode(out)
	})
	srv := &http.Server{Addr: *addr, Handler: mux}
	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	log.Printf("turnaround-time watcher listening on %s (deadline %ds, reversal opens %ds before it)", *addr, cfg.TatSeconds, cfg.ReverseMarginSeconds)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
