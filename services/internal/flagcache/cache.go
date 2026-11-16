// Package flagcache keeps a local copy of every bank's health flag, refreshed
// from the ledger's commit events. Routing decisions read only this copy, so
// they never wait on the ledger.
package flagcache

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	"clearchain/services/internal/fabric"
	"clearchain/services/internal/health"
)

const (
	Chaincode = "healthflag"
	eventName = "BankFlagChanged"

	Healthy  = "HEALTHY"
	Degraded = "DEGRADED"
	Probing  = "PROBING"

	FailFast  = "FAIL_FAST"
	HoldRetry = "HOLD_RETRY"
)

// Flag is the part of the on-chain flag a switch routes on.
type Flag struct {
	BankID        string `json:"bankId"`
	Status        string `json:"status"`
	Since         int64  `json:"since"`
	Seq           int    `json:"seq"`
	Reason        string `json:"reason"`
	Mode          string `json:"mode"`
	HoldMs        int    `json:"holdMs"`
	ProbeShareBps int    `json:"probeShareBps"`
	TxID          string `json:"txId"`
}

// Entry is a cached flag plus when and how this process learned of it.
type Entry struct {
	Flag
	UpdatedAt time.Time `json:"updatedAt"` // local time the cache took this value
	// StatusAt is the local time the bank entered its current status. Probe
	// measurements are taken from this moment.
	StatusAt time.Time `json:"statusAt"`
	Source   string    `json:"source"` // "startup" or "event"
	Block    uint64    `json:"block"`
}

// Cache is safe for concurrent use.
type Cache struct {
	mu      sync.RWMutex
	flags   map[string]Entry
	changed chan struct{} // closed and replaced on every update
}

func New() *Cache {
	return &Cache{flags: map[string]Entry{}, changed: make(chan struct{})}
}

// Get returns the cached flag for a bank. It takes a read lock only.
func (c *Cache) Get(bank string) (Entry, bool) {
	c.mu.RLock()
	e, ok := c.flags[bank]
	c.mu.RUnlock()
	return e, ok
}

// All returns every cached flag, sorted by bank.
func (c *Cache) All() []Entry {
	c.mu.RLock()
	out := make([]Entry, 0, len(c.flags))
	for _, e := range c.flags {
		out = append(out, e)
	}
	c.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].BankID < out[j].BankID })
	return out
}

// Changed returns a channel that is closed at the next cache update.
func (c *Cache) Changed() <-chan struct{} {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.changed
}

func (c *Cache) put(f Flag, source string, block uint64) bool {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	old, ok := c.flags[f.BankID]
	if ok && f.Seq <= old.Seq {
		return false // stale or duplicate
	}
	e := Entry{Flag: f, UpdatedAt: now, StatusAt: now, Source: source, Block: block}
	if ok && old.Status == f.Status {
		e.StatusAt = old.StatusAt
	}
	c.flags[f.BankID] = e
	close(c.changed)
	c.changed = make(chan struct{})
	return true
}

// Start fills the cache from the ledger, then follows commit events until ctx
// ends. The event subscription is opened before the initial read so no change
// can fall between the two. org is the org this process acts for.
func (c *Cache) Start(ctx context.Context, org, channel string) error {
	// Lite peers keep no ledger, so events come from the committing peer.
	evClient, err := fabric.ConnectCommitter(org, "User1", channel)
	if err != nil {
		return err
	}
	events, err := evClient.Network.ChaincodeEvents(ctx, Chaincode)
	if err != nil {
		evClient.Close()
		return fmt.Errorf("subscribe to flag events: %w", err)
	}

	reader, err := fabric.Connect(org, "User1", channel)
	if err != nil {
		evClient.Close()
		return err
	}
	raw, err := reader.Contract(Chaincode).EvaluateTransaction("GetAllFlags")
	reader.Close()
	if err != nil {
		evClient.Close()
		return fmt.Errorf("read flags: %w", err)
	}
	var flags []Flag
	if err := json.Unmarshal(raw, &flags); err != nil {
		evClient.Close()
		return fmt.Errorf("decode flags: %w", err)
	}
	for _, f := range flags {
		c.put(f, "startup", 0)
	}

	go func() {
		defer evClient.Close()
		for ev := range events {
			if ev.EventName != eventName {
				continue
			}
			var f Flag
			if err := json.Unmarshal(ev.Payload, &f); err != nil {
				log.Printf("flagcache: bad event payload in block %d: %v", ev.BlockNumber, err)
				continue
			}
			if c.put(f, "event", ev.BlockNumber) {
				log.Printf("flagcache: %s is now %s (seq %d, mode %s, block %d)", f.BankID, f.Status, f.Seq, f.Mode, ev.BlockNumber)
			}
		}
		if ctx.Err() == nil {
			log.Printf("flagcache: event stream ended unexpectedly; routing continues on the last known flags")
		}
	}()
	return nil
}

// LoadConfig reads the chaincode's thresholds.
func LoadConfig(org, channel string) (health.Config, error) {
	var cfg health.Config
	cl, err := fabric.Connect(org, "User1", channel)
	if err != nil {
		return cfg, err
	}
	defer cl.Close()
	raw, err := cl.Contract(Chaincode).EvaluateTransaction("GetConfig")
	if err != nil {
		return cfg, fmt.Errorf("read config: %w", err)
	}
	return cfg, json.Unmarshal(raw, &cfg)
}
