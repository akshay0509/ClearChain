// Command operator is the network operator's hop (NPCI's role): every request
// a switch sends to a bank passes through it. It measures each bank from that
// traffic, independently of any switch, and records what it sees on the ledger.
// The healthflag chaincode uses this record to confirm or overrule a switch
// that proposes flagging a bank.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"time"

	"clearchain/services/internal/flagcache"
	"clearchain/services/internal/health"
	"clearchain/services/internal/observer"
)

func main() {
	addr := flag.String("addr", ":9102", "listen address")
	banks := flag.String("banks", "http://localhost:9101", "bank systems base URL")
	channel := flag.String("channel", "mychannel", "channel name")
	window := flag.Duration("window", 5*time.Second, "measurement window")
	timeout := flag.Duration("bank-timeout", 3*time.Second, "how long to wait for a bank")
	settlementFile := flag.String("settlement-file", "", "CSV to append every credit a bank confirmed to (rrn,bank,amount)")
	flag.Parse()

	// The operator's own record of what was settled, kept outside the ledger.
	// Reconciliation compares it with the ledger using plain SQL.
	var fileMu sync.Mutex
	var out *os.File
	if *settlementFile != "" {
		f, err := os.OpenFile(*settlementFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			log.Fatalf("operator: %v", err)
		}
		defer f.Close()
		out = f
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	cache := flagcache.New()
	if err := cache.Start(ctx, "npci", *channel); err != nil {
		log.Fatalf("operator: %v", err)
	}
	meter := health.NewMeter()
	obs := &observer.Observer{Org: "npci", Channel: *channel, Window: *window, Meter: meter, Cache: cache}
	go func() {
		if err := obs.Run(ctx); err != nil {
			log.Fatalf("operator: observer: %v", err)
		}
	}()

	client := &http.Client{Timeout: *timeout}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /upi/{bank}/{op}", func(w http.ResponseWriter, r *http.Request) {
		bank, op := r.PathValue("bank"), r.PathValue("op")
		body, _ := io.ReadAll(r.Body)
		start := time.Now()
		resp, err := client.Post(*banks+"/bank/"+bank+"/"+op, "application/json", bytes.NewReader(body))
		elapsed := time.Since(start)

		timedOut := err != nil && isTimeout(err)
		if err == nil && resp.StatusCode == http.StatusGatewayTimeout {
			timedOut = true
		}
		failed := err != nil || resp.StatusCode >= 400
		// Reversals and float payouts are housekeeping, not a measure of the bank's payment path.
		if op == "debit" || op == "credit" {
			meter.Record(bank, elapsed, timedOut, failed)
		}
		if out != nil && op == "credit" && !failed {
			var req struct {
				RRN    string `json:"rrn"`
				Amount int64  `json:"amount"`
			}
			if json.Unmarshal(body, &req) == nil {
				fileMu.Lock()
				fmt.Fprintln(out, req.RRN+","+bank+","+strconv.FormatInt(req.Amount, 10))
				fileMu.Unlock()
			}
		}

		switch {
		case timedOut:
			http.Error(w, "bank timeout", http.StatusGatewayTimeout)
		case err != nil:
			http.Error(w, "bank unreachable", http.StatusBadGateway)
		default:
			defer resp.Body.Close()
			w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
			w.WriteHeader(resp.StatusCode)
			io.Copy(w, resp.Body)
		}
		if err == nil && timedOut {
			resp.Body.Close()
		}
	})
	srv := &http.Server{Addr: *addr, Handler: mux}
	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	log.Printf("network operator hop listening on %s, banks at %s", *addr, *banks)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func isTimeout(err error) bool {
	var t interface{ Timeout() bool }
	return errors.As(err, &t) && t.Timeout()
}
