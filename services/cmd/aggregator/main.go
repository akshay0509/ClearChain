// Command aggregator is the payment aggregator's settlement service. It
// follows the ledger for escrow credits the sponsor bank has confirmed and, for
// each merchant payment, asks the settlement chaincode to pay the merchant
// from the float at once instead of waiting for the next settlement cycle.
//
// It never decides whether a payout is allowed. The chaincode does that: the
// credit must be confirmed, the payment not already paid out, the float sufficient.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"clearchain/services/internal/fabric"
)

const chaincode = "settlement"

func main() {
	addr := flag.String("addr", ":9104", "listen address")
	channel := flag.String("channel", "mychannel", "channel name")
	workers := flag.Int("workers", 32, "concurrent payout requests")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	cl, err := fabric.Connect("aggregator", "User1", *channel)
	if err != nil {
		log.Fatalf("aggregator: %v", err)
	}
	defer cl.Close()
	contract := cl.Contract(chaincode)

	events, closeEvents, err := fabric.Events(ctx, "aggregator", *channel, chaincode, false)
	if err != nil {
		log.Fatalf("aggregator: %v", err)
	}
	defer closeEvents()

	type job struct {
		rrn  string
		seen time.Time
	}
	jobs := make(chan job, 200000)
	var paid, failed, duplicates, conflictRetries atomic.Int64
	var mu sync.Mutex
	var commitMs []float64 // confirmation event seen -> payout committed

	for i := 0; i < *workers; i++ {
		go func() {
			for j := range jobs {
				// A payout that loses a race for its float lane is simply sent again.
				_, retries, err := fabric.SubmitRetry(contract, 25, "PayoutMerchant", j.rrn)
				conflictRetries.Add(int64(retries))
				switch {
				case err == nil:
					paid.Add(1)
					mu.Lock()
					commitMs = append(commitMs, float64(time.Since(j.seen).Microseconds())/1000)
					mu.Unlock()
				case fabric.Mentions(err, "duplicate"):
					duplicates.Add(1)
				default:
					failed.Add(1)
					log.Printf("aggregator: payout for %s refused: %s", j.rrn, fabric.Describe(err))
				}
			}
		}()
	}

	go func() {
		for ev := range events {
			if ev.EventName != "CreditConfirmed" {
				continue
			}
			var c struct {
				RRN        string `json:"rrn"`
				MerchantID string `json:"merchantId"`
			}
			if json.Unmarshal(ev.Payload, &c) != nil || c.MerchantID == "" {
				continue // not a merchant payment: nothing for the aggregator to settle
			}
			jobs <- job{rrn: c.RRN, seen: time.Now()}
		}
	}()

	mux := http.NewServeMux()
	// Ask for a payout directly; used to show that the chaincode refuses a repeat.
	mux.HandleFunc("POST /payout/{rrn}", func(w http.ResponseWriter, r *http.Request) {
		res, err := contract.SubmitTransaction("PayoutMerchant", r.PathValue("rrn"))
		if err != nil {
			http.Error(w, fabric.Describe(err), http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(res)
	})
	mux.HandleFunc("GET /state", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		d := append([]float64(nil), commitMs...)
		mu.Unlock()
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
			"payoutsCommitted":    paid.Load(),
			"payoutsRefused":      failed.Load(),
			"duplicatesRefused":   duplicates.Load(),
			"waiting":             len(jobs),
			"laneConflictRetries": conflictRetries.Load(),
			"confirmedToCommitMs": map[string]any{"samples": len(d), "p50": pct(0.50), "p95": pct(0.95), "max": pct(1)},
		})
	})
	srv := &http.Server{Addr: *addr, Handler: mux}
	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	log.Printf("aggregator settlement service listening on %s", *addr)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
