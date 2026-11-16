// Package observer publishes what one org measures about each bank to the
// healthflag chaincode. The switch and the network operator each run one, from
// their own traffic, so the ledger holds two independent views of every bank.
package observer

import (
	"context"
	"log"
	"strconv"
	"sync"
	"time"

	"clearchain/services/internal/fabric"
	"clearchain/services/internal/flagcache"
	"clearchain/services/internal/health"
)

// Report is what the observer measured for one bank in one tick.
type Report struct {
	Bank      string
	Flag      flagcache.Entry
	Stats     health.Stats
	Submitted bool // an observation was committed this tick
}

type Observer struct {
	Org     string
	Channel string
	Window  time.Duration
	Meter   *health.Meter
	Cache   *flagcache.Cache
	// AfterTick, if set, runs once per tick after the observations are committed.
	AfterTick func(ctx context.Context, reports []Report)
}

// Run submits an observation per bank every Window until ctx ends.
func (o *Observer) Run(ctx context.Context) error {
	cl, err := fabric.Connect(o.Org, "User1", o.Channel)
	if err != nil {
		return err
	}
	defer cl.Close()
	contract := cl.Contract(flagcache.Chaincode)

	ticker := time.NewTicker(o.Window)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		now := time.Now()
		entries := o.Cache.All()
		reports := make([]Report, len(entries))
		var wg sync.WaitGroup
		for i, e := range entries {
			// Normally report the last window. While a bank is being probed only a
			// trickle of payments reaches it, so report everything since probing began.
			from := now.Add(-o.Window)
			if e.Status == flagcache.Probing {
				from = e.StatusAt
			}
			st := o.Meter.Since(e.BankID, from)
			reports[i] = Report{Bank: e.BankID, Flag: e, Stats: st}
			if st.TxCount == 0 {
				continue
			}
			wg.Add(1)
			go func(r *Report, windowSec int) {
				defer wg.Done()
				_, err := contract.SubmitTransaction("SubmitObservation", r.Bank,
					strconv.Itoa(windowSec), strconv.Itoa(r.Stats.TxCount), strconv.Itoa(r.Stats.TimeoutCount),
					strconv.Itoa(r.Stats.FailureCount), strconv.Itoa(r.Stats.P95LatencyMs))
				if err != nil {
					log.Printf("observer[%s]: submit observation for %s failed: %v", o.Org, r.Bank, err)
					return
				}
				r.Submitted = true
			}(&reports[i], max(1, int(now.Sub(from).Seconds())))
		}
		wg.Wait()
		if o.AfterTick != nil {
			o.AfterTick(ctx, reports)
		}
		o.Meter.Prune(now.Add(-10 * time.Minute))
	}
}
