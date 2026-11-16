// Command loadgen sends a steady stream of payments to a switch and prints,
// second by second, what happened to them.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

type result struct {
	Outcome string `json:"outcome"`
	Reason  string `json:"reason"`
	Debited bool   `json:"debited"`
}

func main() {
	target := flag.String("switch", "http://localhost:9100", "switch base URL")
	rate := flag.Int("rate", 30, "payments per second")
	duration := flag.Duration("duration", 30*time.Second, "how long to run")
	payer := flag.String("payer", "BANKA", "payer bank")
	payee := flag.String("payee", "BANKB", "payee bank")
	prefix := flag.String("prefix", "", "RRN prefix (default: derived from the start time)")
	flag.Parse()

	if *prefix == "" {
		*prefix = fmt.Sprintf("R%d", time.Now().Unix()%1000000)
	}
	client := &http.Client{Timeout: 60 * time.Second, Transport: &http.Transport{MaxIdleConnsPerHost: 512}}

	var mu sync.Mutex
	second := map[string]int{}
	total := map[string]int{}
	stuck := 0 // debited without confirmation
	var wg sync.WaitGroup

	send := func(n int) {
		defer wg.Done()
		body, _ := json.Marshal(map[string]any{"rrn": fmt.Sprintf("%s%07d", *prefix, n), "payerBank": *payer, "payeeBank": *payee, "amount": 10000 + n%5000})
		key := "ERROR"
		var r result
		resp, err := client.Post(*target+"/pay", "application/json", bytes.NewReader(body))
		if err == nil {
			if json.NewDecoder(resp.Body).Decode(&r) == nil && r.Outcome != "" {
				key = r.Outcome
				if r.Reason != "" && r.Outcome != "SUCCESS" {
					key += ":" + r.Reason
				}
			}
			resp.Body.Close()
		}
		mu.Lock()
		second[key]++
		total[key]++
		if r.Outcome == "PENDING" {
			stuck++
		}
		mu.Unlock()
	}

	start := time.Now()
	tick := time.NewTicker(time.Second / time.Duration(*rate))
	report := time.NewTicker(time.Second)
	defer tick.Stop()
	defer report.Stop()
	n := 0
	for time.Since(start) < *duration {
		select {
		case <-tick.C:
			n++
			wg.Add(1)
			go send(n)
		case <-report.C:
			mu.Lock()
			fmt.Printf("t=%3ds  %s\n", int(time.Since(start).Seconds()), summary(second))
			second = map[string]int{}
			mu.Unlock()
		}
	}
	wg.Wait()
	fmt.Printf("\nsent %d payments in %v\n", n, time.Since(start).Round(time.Second))
	fmt.Printf("totals: %s\n", summary(total))
	fmt.Printf("debited but unconfirmed (stuck): %d\n", stuck)
}

func summary(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, m[k]))
	}
	if len(parts) == 0 {
		return "(nothing completed)"
	}
	return strings.Join(parts, "  ")
}
