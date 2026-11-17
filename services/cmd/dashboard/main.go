// Command dashboard serves ClearChain's web dashboard: bank health and routing,
// supply-chain payment flow, and fraud tracing and holds.
//
// It reads YugabyteDB directly. Access is by org: the viewer chooses which
// org they are, and the dashboard reads only that org's own state database.
// Drunix keeps each private data collection only in its member orgs'
// databases, so a viewer sees exactly what their org's peers hold, and a
// non-member sees that private records exist but not what is in them.
//
// Live routing decisions come from the switches' own /state endpoints.
package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed web
var webFS embed.FS

const schema = "mychannel"

// org is one network member and its state database, published on the host.
type org struct {
	Name   string `json:"name"`
	Label  string `json:"label"`
	MSP    string `json:"msp"`
	DBPort int    `json:"dbPort"`
}

var orgs = []org{
	{"switch", "Switch", "SwitchOrgMSP", 5433},
	{"npci", "NPCI (network operator)", "NPCIOrgMSP", 5434},
	{"banka", "Bank A", "BankAOrgMSP", 5435},
	{"bankb", "Bank B", "BankBOrgMSP", 5436},
	{"aggregator", "Aggregator", "AggregatorOrgMSP", 5437},
	{"sponsorbank", "Sponsor Bank", "SponsorBankOrgMSP", 5438},
	{"buyer", "Buyer", "BuyerOrgMSP", 5439},
	{"investigator", "Investigator", "InvestigatorOrgMSP", 5440},
}

func findOrg(name string) (org, bool) {
	for _, o := range orgs {
		if o.Name == name {
			return o, true
		}
	}
	return org{}, false
}

type server struct {
	host     string
	switches []string
	http     *http.Client

	mu    sync.Mutex
	pools map[string]*pgxpool.Pool
}

func (s *server) pool(ctx context.Context, o org) (*pgxpool.Pool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.pools[o.Name]; p != nil {
		return p, nil
	}
	dsn := fmt.Sprintf("postgres://yugabyte:yugabyte@%s:%d/yugabyte?sslmode=disable&pool_max_conns=4", s.host, o.DBPort)
	p, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	s.pools[o.Name] = p
	return p, nil
}

// doc is one state entry: its chaincode key and JSON value.
type doc struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

// readTable returns every entry in one chaincode's table (or one of its
// private collections), with the table prefix stripped from each key.
func readTable(ctx context.Context, p *pgxpool.Pool, table string) ([]doc, error) {
	rows, err := p.Query(ctx, fmt.Sprintf(`select key, db_value from %s."%s"`, schema, table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []doc
	for rows.Next() {
		var key []byte
		var val []byte
		if err := rows.Scan(&key, &val); err != nil {
			return nil, err
		}
		// Drunix stores keys as "d<table>\x00<chaincode key>".
		if i := bytes.IndexByte(key, 0); i >= 0 {
			key = key[i+1:]
		}
		if len(val) == 0 {
			val = []byte("null")
		}
		out = append(out, doc{Key: string(key), Value: val})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, rows.Err()
}

func tables(ctx context.Context, p *pgxpool.Pool) (map[string]bool, error) {
	rows, err := p.Query(ctx, `select table_name from information_schema.tables where table_schema = $1`, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out[n] = true
	}
	return out, rows.Err()
}

// byPrefix groups entries by the part of the key before the first ':'.
func byPrefix(docs []doc) map[string][]json.RawMessage {
	out := map[string][]json.RawMessage{}
	for _, d := range docs {
		prefix := d.Key
		if i := strings.IndexByte(prefix, ':'); i >= 0 {
			prefix = prefix[:i]
		}
		out[prefix] = append(out[prefix], d.Value)
	}
	return out
}

// settlementSummary aggregates in SQL rather than shipping every payment.
func settlementSummary(ctx context.Context, p *pgxpool.Pool) (map[string]any, error) {
	out := map[string]any{}
	byStatus := map[string]map[string]int64{}
	rows, err := p.Query(ctx, `select db_value->>'status', count(*), coalesce(sum((db_value->>'amount')::bigint), 0)
		from mychannel.settlement where db_value->>'docType' = 'payment' group by 1`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var st string
		var n, amt int64
		if err := rows.Scan(&st, &n, &amt); err != nil {
			rows.Close()
			return nil, err
		}
		byStatus[st] = map[string]int64{"count": n, "amount": amt}
	}
	rows.Close()
	out["payments"] = byStatus

	var float, lanes int64
	if err := p.QueryRow(ctx, `select coalesce(sum((db_value->>'balance')::bigint), 0), count(*)
		from mychannel.settlement where db_value->>'docType' = 'lane'`).Scan(&float, &lanes); err != nil {
		return nil, err
	}
	out["float"] = map[string]int64{"total": float, "lanes": lanes}

	var n, gross, merchant, fee, gst int64
	if err := p.QueryRow(ctx, `select count(*), coalesce(sum((db_value->>'gross')::bigint), 0),
		coalesce(sum((db_value->>'merchantAmount')::bigint), 0), coalesce(sum((db_value->>'fee')::bigint), 0),
		coalesce(sum((db_value->>'gst')::bigint), 0)
		from mychannel.settlement where db_value->>'docType' = 'payout'`).Scan(&n, &gross, &merchant, &fee, &gst); err != nil {
		return nil, err
	}
	out["payouts"] = map[string]int64{"count": n, "gross": gross, "merchant": merchant, "fee": fee, "gst": gst}

	rows, err = p.Query(ctx, `select db_value from mychannel.settlement where db_value->>'docType' = 'reversal'
		order by (db_value->>'reversedAt')::bigint desc limit 8`)
	if err != nil {
		return nil, err
	}
	var revs []json.RawMessage
	for rows.Next() {
		var v []byte
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return nil, err
		}
		revs = append(revs, v)
	}
	rows.Close()
	out["reversals"] = revs
	return out, nil
}

func (s *server) state(ctx context.Context, o org) (map[string]any, error) {
	p, err := s.pool(ctx, o)
	if err != nil {
		return nil, err
	}
	present, err := tables(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("%s's database on port %d: %w", o.Label, o.DBPort, err)
	}
	out := map[string]any{"org": o, "readAt": time.Now().UTC().Format(time.RFC3339)}

	// Private collections in this org's database, and those it knows of only by hash.
	held, hashedOnly := []string{}, []string{}
	for t := range present {
		if i := strings.Index(t, "$$p"); i > 0 {
			held = append(held, t)
		}
	}
	for t := range present {
		if i := strings.Index(t, "$$h"); i > 0 && !present[t[:i]+"$$p"+t[i+3:]] {
			hashedOnly = append(hashedOnly, t)
		}
	}
	sort.Strings(held)
	sort.Strings(hashedOnly)
	out["privateHeld"], out["privateHashedOnly"] = held, hashedOnly

	load := func(table string) ([]doc, error) {
		if !present[table] {
			return nil, nil
		}
		return readTable(ctx, p, table)
	}

	hf, err := load("healthflag")
	if err != nil {
		return nil, err
	}
	out["healthflag"] = byPrefix(hf)

	if present["settlement"] {
		sum, err := settlementSummary(ctx, p)
		if err != nil {
			return nil, err
		}
		out["settlement"] = sum
	}

	sc, err := load("supplychain")
	if err != nil {
		return nil, err
	}
	scPriv, err := load("supplychain$$pscTerms")
	if err != nil {
		return nil, err
	}
	out["supplychain"] = map[string]any{"public": byPrefix(sc), "private": byPrefix(scPriv), "member": present["supplychain$$pscTerms"]}

	fh, err := load("fraudhold")
	if err != nil {
		return nil, err
	}
	details := map[string]json.RawMessage{}
	for _, t := range held {
		if !strings.HasPrefix(t, "fraudhold$$p") {
			continue
		}
		docs, err := load(t)
		if err != nil {
			return nil, err
		}
		for _, d := range docs {
			details[d.Key] = d.Value
		}
	}
	out["fraudhold"] = map[string]any{"public": byPrefix(fh), "details": details}
	return out, nil
}

// switchStates fetches each switch's live state; a switch that is not
// running is reported as such rather than failing the page.
func (s *server) switchStates() []map[string]any {
	out := make([]map[string]any, len(s.switches))
	var wg sync.WaitGroup
	for i, u := range s.switches {
		wg.Add(1)
		go func(i int, u string) {
			defer wg.Done()
			row := map[string]any{"url": u, "up": false}
			resp, err := s.http.Get(u + "/state")
			if err == nil {
				var st map[string]any
				if json.NewDecoder(resp.Body).Decode(&st) == nil {
					if r, ok := st["recent"].([]any); ok && len(r) > 10 {
						st["recent"] = r[len(r)-10:]
					}
					row["up"], row["state"] = true, st
				}
				resp.Body.Close()
			}
			out[i] = row
		}(i, u)
	}
	wg.Wait()
	return out
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	host := flag.String("db-host", "localhost", "host where the org databases are published")
	switches := flag.String("switches", "http://localhost:9100,http://localhost:9110", "switch base URLs, comma-separated")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	s := &server{host: *host, http: &http.Client{Timeout: 800 * time.Millisecond}, pools: map[string]*pgxpool.Pool{}}
	for _, u := range strings.Split(*switches, ",") {
		if u = strings.TrimSpace(u); u != "" {
			s.switches = append(s.switches, strings.TrimSuffix(u, "/"))
		}
	}

	web, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServer(http.FS(web)))
	mux.HandleFunc("GET /api/orgs", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, orgs)
	})
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		o, ok := findOrg(r.URL.Query().Get("org"))
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown org"})
			return
		}
		qctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		st, err := s.state(qctx, o)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, st)
	})
	mux.HandleFunc("GET /api/switches", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.switchStates())
	})

	srv := &http.Server{Addr: *addr, Handler: mux}
	go func() {
		<-ctx.Done()
		srv.Close()
		s.mu.Lock()
		for _, p := range s.pools {
			p.Close()
		}
		s.mu.Unlock()
	}()
	log.Printf("ClearChain dashboard on http://localhost%s (databases on %s, switches %v)", *addr, *host, s.switches)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
