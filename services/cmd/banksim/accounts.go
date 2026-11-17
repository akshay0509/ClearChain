package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"sync"
)

// account is a customer account in a bank's core system. Liens mark money that
// may not leave the account; everything else stays usable.
type account struct {
	Balance int64            `json:"balance"`
	Liens   map[string]int64 `json:"liens"`  // by hold ID
	Frozen  bool             `json:"frozen"` // a whole-account freeze, for comparison
}

func (a *account) liened() int64 {
	var sum int64
	for _, v := range a.Liens {
		sum += v
	}
	return sum
}

func (a *account) usable() int64 {
	if a.Frozen {
		return 0
	}
	if u := a.Balance - a.liened(); u > 0 {
		return u
	}
	return 0
}

type accounts struct {
	mu   sync.Mutex
	byID map[string]*account // bank + "/" + account hash
}

func (s *server) acct(bank, hash string) *account {
	s.accts.mu.Lock()
	defer s.accts.mu.Unlock()
	key := bank + "/" + hash
	a := s.accts.byID[key]
	if a == nil {
		a = &account{Liens: map[string]int64{}}
		s.accts.byID[key] = a
	}
	return a
}

type acctReq struct {
	AccountHash string `json:"accountHash"`
	Amount      int64  `json:"amount"`
	HoldID      string `json:"holdId"`
	ToBank      string `json:"toBank"`
	ToAccount   string `json:"toAccountHash"`
}

func (s *server) decodeAcct(w http.ResponseWriter, r *http.Request) (string, acctReq, bool) {
	bank := r.PathValue("bank")
	var req acctReq
	if s.bank(bank) == nil || json.NewDecoder(r.Body).Decode(&req) != nil || req.AccountHash == "" {
		http.Error(w, "unknown bank, or accountHash missing", http.StatusBadRequest)
		return "", req, false
	}
	return bank, req, true
}

func (s *server) acctView(bank, hash string) map[string]any {
	a := s.acct(bank, hash)
	s.accts.mu.Lock()
	defer s.accts.mu.Unlock()
	ids := make([]string, 0, len(a.Liens))
	for id := range a.Liens {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return map[string]any{"bank": bank, "accountHash": hash, "balance": a.Balance, "liened": a.liened(),
		"usable": a.usable(), "frozen": a.Frozen, "lienHolds": ids}
}

func (s *server) registerAccountRoutes(mux *http.ServeMux) {
	// Set an account's opening balance.
	mux.HandleFunc("POST /bank/{bank}/account", func(w http.ResponseWriter, r *http.Request) {
		bank, req, ok := s.decodeAcct(w, r)
		if !ok {
			return
		}
		a := s.acct(bank, req.AccountHash)
		s.accts.mu.Lock()
		a.Balance = req.Amount
		s.accts.mu.Unlock()
		writeJSON(w, s.acctView(bank, req.AccountHash))
	})
	mux.HandleFunc("GET /bank/{bank}/account/{hash}", func(w http.ResponseWriter, r *http.Request) {
		if s.bank(r.PathValue("bank")) == nil {
			http.Error(w, "unknown bank", http.StatusNotFound)
			return
		}
		writeJSON(w, s.acctView(r.PathValue("bank"), r.PathValue("hash")))
	})
	// Move money between two accounts; the debit is refused if it would touch liened money.
	mux.HandleFunc("POST /bank/{bank}/transfer", func(w http.ResponseWriter, r *http.Request) {
		bank, req, ok := s.decodeAcct(w, r)
		if !ok {
			return
		}
		if s.bank(req.ToBank) == nil || req.ToAccount == "" || req.Amount <= 0 {
			http.Error(w, "toBank, toAccountHash and a positive amount are required", http.StatusBadRequest)
			return
		}
		from, to := s.acct(bank, req.AccountHash), s.acct(req.ToBank, req.ToAccount)
		s.accts.mu.Lock()
		if req.Amount > from.usable() {
			usable := from.usable()
			s.accts.mu.Unlock()
			http.Error(w, fmt.Sprintf("refused: only %d paise of this account is usable", usable), http.StatusForbidden)
			return
		}
		from.Balance -= req.Amount
		to.Balance += req.Amount
		s.accts.mu.Unlock()
		writeJSON(w, s.acctView(bank, req.AccountHash))
	})
	// Mark a lien for one hold.
	mux.HandleFunc("POST /bank/{bank}/lien", func(w http.ResponseWriter, r *http.Request) {
		bank, req, ok := s.decodeAcct(w, r)
		if !ok {
			return
		}
		a := s.acct(bank, req.AccountHash)
		s.accts.mu.Lock()
		a.Liens[req.HoldID] = req.Amount
		s.accts.mu.Unlock()
		writeJSON(w, s.acctView(bank, req.AccountHash))
	})
	// Freeze or unfreeze the whole account: what banks do today without targeted holds.
	mux.HandleFunc("POST /bank/{bank}/freeze", func(w http.ResponseWriter, r *http.Request) {
		bank, req, ok := s.decodeAcct(w, r)
		if !ok {
			return
		}
		a := s.acct(bank, req.AccountHash)
		s.accts.mu.Lock()
		a.Frozen = req.Amount != 0
		s.accts.mu.Unlock()
		writeJSON(w, s.acctView(bank, req.AccountHash))
	})
	// Carry out a court order: take the recovered amount out of the account
	// and lift every lien on it.
	mux.HandleFunc("POST /bank/{bank}/recover", func(w http.ResponseWriter, r *http.Request) {
		bank, req, ok := s.decodeAcct(w, r)
		if !ok {
			return
		}
		a := s.acct(bank, req.AccountHash)
		s.accts.mu.Lock()
		if req.Amount > a.Balance || req.Amount > a.liened() {
			s.accts.mu.Unlock()
			http.Error(w, "recovery exceeds the liened balance", http.StatusConflict)
			return
		}
		a.Balance -= req.Amount
		a.Liens = map[string]int64{}
		s.accts.mu.Unlock()
		writeJSON(w, s.acctView(bank, req.AccountHash))
	})
	// Credit an account, e.g. a victim's share of recovered money.
	mux.HandleFunc("POST /bank/{bank}/deposit", func(w http.ResponseWriter, r *http.Request) {
		bank, req, ok := s.decodeAcct(w, r)
		if !ok {
			return
		}
		a := s.acct(bank, req.AccountHash)
		s.accts.mu.Lock()
		a.Balance += req.Amount
		s.accts.mu.Unlock()
		writeJSON(w, s.acctView(bank, req.AccountHash))
	})
}
