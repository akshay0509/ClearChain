// Command banksim stands in for the core banking systems of the banks on the
// network. Each bank answers debit, credit and reversal requests, and can be
// switched between healthy and degraded behaviour to rehearse an outage.
//
// It also keeps a per-payment record of what each bank actually did, so a demo
// can show that a payment declined by the switch was never debited.
package main

import (
	"encoding/json"
	"flag"
	"log"
	"math/rand"
	"net/http"
	"sort"
	"sync"
	"time"
)

// behaviour is how a bank responds while in a given mode.
type behaviour struct {
	MinLatencyMs int     `json:"minLatencyMs"`
	MaxLatencyMs int     `json:"maxLatencyMs"`
	HangRate     float64 `json:"hangRate"` // share of requests that never answer in time
	FailRate     float64 `json:"failRate"` // share of requests answered with an error
}

var presets = map[string]behaviour{
	"healthy":  {MinLatencyMs: 20, MaxLatencyMs: 90, HangRate: 0, FailRate: 0.002},
	"degraded": {MinLatencyMs: 1200, MaxLatencyMs: 2800, HangRate: 0.30, FailRate: 0.10},
	"down":     {MinLatencyMs: 0, MaxLatencyMs: 0, HangRate: 1, FailRate: 0},
}

type entry struct {
	Debited  bool `json:"debited"`
	Credited bool `json:"credited"`
	Reversed bool `json:"reversed"`
}

type bank struct {
	mu       sync.Mutex
	Mode     string
	payments map[string]*entry // by RRN
	requests int
}

type server struct {
	mu    sync.RWMutex
	banks map[string]*bank
	hang  time.Duration
}

func (s *server) bank(id string) *bank {
	s.mu.RLock()
	b := s.banks[id]
	s.mu.RUnlock()
	return b
}

type payReq struct {
	RRN    string `json:"rrn"`
	Amount int64  `json:"amount"` // paise
}

func (s *server) handleOp(w http.ResponseWriter, r *http.Request) {
	b := s.bank(r.PathValue("bank"))
	op := r.PathValue("op")
	if b == nil || (op != "debit" && op != "credit" && op != "reverse") {
		http.Error(w, "unknown bank or operation", http.StatusNotFound)
		return
	}
	var req payReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RRN == "" {
		http.Error(w, "rrn required", http.StatusBadRequest)
		return
	}
	b.mu.Lock()
	beh := presets[b.Mode]
	b.requests++
	b.mu.Unlock()

	// Reversals are housekeeping and always honoured promptly.
	if op != "reverse" {
		if rand.Float64() < beh.HangRate {
			select {
			case <-time.After(s.hang):
			case <-r.Context().Done():
			}
			http.Error(w, "timeout", http.StatusGatewayTimeout)
			return
		}
		lat := beh.MinLatencyMs
		if beh.MaxLatencyMs > beh.MinLatencyMs {
			lat += rand.Intn(beh.MaxLatencyMs - beh.MinLatencyMs)
		}
		time.Sleep(time.Duration(lat) * time.Millisecond)
		if rand.Float64() < beh.FailRate {
			http.Error(w, "core banking error", http.StatusBadGateway)
			return
		}
	}

	b.mu.Lock()
	e := b.payments[req.RRN]
	if e == nil {
		e = &entry{}
		b.payments[req.RRN] = e
	}
	switch op {
	case "debit":
		e.Debited = true
	case "credit":
		e.Credited = true
	case "reverse":
		e.Reversed = true
	}
	b.mu.Unlock()
	writeJSON(w, map[string]string{"status": "OK", "rrn": req.RRN})
}

// handleMode switches a bank's behaviour: {"mode":"degraded"}.
func (s *server) handleMode(w http.ResponseWriter, r *http.Request) {
	b := s.bank(r.PathValue("bank"))
	var body struct {
		Mode string `json:"mode"`
	}
	if b == nil || json.NewDecoder(r.Body).Decode(&body) != nil {
		http.Error(w, "unknown bank or bad body", http.StatusBadRequest)
		return
	}
	if _, ok := presets[body.Mode]; !ok {
		http.Error(w, "mode must be healthy, degraded or down", http.StatusBadRequest)
		return
	}
	b.mu.Lock()
	b.Mode = body.Mode
	b.mu.Unlock()
	log.Printf("%s is now %s", r.PathValue("bank"), body.Mode)
	writeJSON(w, map[string]string{"bank": r.PathValue("bank"), "mode": body.Mode})
}

// handlePayment reports what a bank did for one RRN.
func (s *server) handlePayment(w http.ResponseWriter, r *http.Request) {
	b := s.bank(r.PathValue("bank"))
	if b == nil {
		http.Error(w, "unknown bank", http.StatusNotFound)
		return
	}
	b.mu.Lock()
	e := b.payments[r.PathValue("rrn")]
	out := entry{}
	if e != nil {
		out = *e
	}
	b.mu.Unlock()
	writeJSON(w, out)
}

func (s *server) handleState(w http.ResponseWriter, r *http.Request) {
	type row struct {
		Bank     string `json:"bank"`
		Mode     string `json:"mode"`
		Requests int    `json:"requests"`
		Debits   int    `json:"debits"`
		Credits  int    `json:"credits"`
		Reversed int    `json:"reversed"`
	}
	var rows []row
	s.mu.RLock()
	for id, b := range s.banks {
		b.mu.Lock()
		rw := row{Bank: id, Mode: b.Mode, Requests: b.requests}
		for _, e := range b.payments {
			if e.Debited {
				rw.Debits++
			}
			if e.Credited {
				rw.Credits++
			}
			if e.Reversed {
				rw.Reversed++
			}
		}
		b.mu.Unlock()
		rows = append(rows, rw)
	}
	s.mu.RUnlock()
	sort.Slice(rows, func(i, j int) bool { return rows[i].Bank < rows[j].Bank })
	writeJSON(w, rows)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func main() {
	addr := flag.String("addr", ":9101", "listen address")
	hang := flag.Duration("hang", 8*time.Second, "how long a hung request stays silent")
	flag.Parse()

	s := &server{banks: map[string]*bank{}, hang: *hang}
	for _, id := range []string{"BANKA", "BANKB"} {
		s.banks[id] = &bank{Mode: "healthy", payments: map[string]*entry{}}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /bank/{bank}/{op}", s.handleOp)
	mux.HandleFunc("GET /bank/{bank}/payment/{rrn}", s.handlePayment)
	mux.HandleFunc("POST /admin/bank/{bank}/mode", s.handleMode)
	mux.HandleFunc("GET /admin/state", s.handleState)
	log.Printf("bank simulator listening on %s (BANKA, BANKB)", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
