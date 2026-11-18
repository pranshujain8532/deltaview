// Command dvbench runs the benchmarks in docs/benchmarks.md against the
// Docker Postgres and writes one CSV per benchmark to bench/results.
// It embeds the engine in-process so it can time each transaction exactly.
//
//	go run ./cmd/dvbench                       all benchmarks, default sizes
//	go run ./cmd/dvbench -only refresh -sizes 100000,1000000,10000000
package main

import (
	"context"
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pranshujain8532/deltaview/internal/cdc"
	"github.com/pranshujain8532/deltaview/internal/engine"
	"github.com/pranshujain8532/deltaview/internal/state"
)

type bench struct {
	pgURL   string
	out     string
	slot    string
	db      *pgx.Conn
	machine string
	quiet   *slog.Logger
	hb      int64
}

func main() {
	var (
		pgURL = flag.String("pg", envOr("DV_PG_URL", "postgres://deltaview:deltaview@localhost:5432/deltaview?sslmode=disable"), "Postgres URL")
		out   = flag.String("out", "bench/results", "CSV output directory")
		only  = flag.String("only", "", "comma-separated subset: refresh,latency,throughput,groupcommit,statesize,recovery,pgivm")
		sizes = flag.String("sizes", "100000,1000000", "table sizes for the refresh and state size benchmarks")
		chg   = flag.Int("changes", 1000, "changes applied in the refresh benchmark")
		txns  = flag.Int("txns", 20000, "pending transactions for throughput and recovery")
	)
	flag.Parse()
	ctx := context.Background()
	db, err := pgx.Connect(ctx, *pgURL)
	if err != nil {
		fatal(err)
	}
	defer db.Close(ctx)
	var pgVersion string
	_ = db.QueryRow(ctx, "SHOW server_version").Scan(&pgVersion)
	b := &bench{pgURL: *pgURL, out: *out, slot: "dvbench_slot", db: db,
		machine: fmt.Sprintf("%s/%s, %d CPUs, Go %s, Postgres %s", runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), runtime.Version(), pgVersion),
		quiet:   slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fatal(err)
	}
	fmt.Println("machine:", b.machine)
	want := func(name string) bool { return *only == "" || strings.Contains(","+*only+",", ","+name+",") }
	var ns []int
	for _, s := range strings.Split(*sizes, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil {
			fatal(err)
		}
		ns = append(ns, n)
	}
	steps := []struct {
		name string
		fn   func(context.Context) error
	}{
		{"refresh", func(ctx context.Context) error { return b.refreshVsIncremental(ctx, ns, *chg) }},
		{"latency", b.latency},
		{"throughput", func(ctx context.Context) error { return b.throughput(ctx, *txns) }},
		{"groupcommit", func(ctx context.Context) error { return b.groupCommit(ctx, *txns) }},
		{"statesize", func(ctx context.Context) error { return b.stateSize(ctx, ns) }},
		{"recovery", func(ctx context.Context) error { return b.recovery(ctx, *txns) }},
		{"pgivm", b.pgIVM},
	}
	for _, s := range steps {
		if !want(s.name) {
			continue
		}
		fmt.Printf("\n== %s\n", s.name)
		if err := s.fn(ctx); err != nil {
			fatal(fmt.Errorf("%s: %w", s.name, err))
		}
	}
	_ = cdc.DropSlot(ctx, db, b.slot)
}

// ---- data loading -------------------------------------------------------

const viewRevenue = `CREATE MATERIALIZED VIEW revenue_by_city AS
SELECT c.city, COUNT(*) AS orders, SUM(o.amount) AS revenue
FROM orders o JOIN customers c ON o.customer_id = c.id WHERE o.status = 'paid' GROUP BY c.city;`

var shapes = []struct{ name, sql string }{
	{"filter", `CREATE MATERIALIZED VIEW v AS SELECT id, amount FROM orders WHERE status = 'paid';`},
	{"aggregate", `CREATE MATERIALIZED VIEW v AS SELECT customer_id, COUNT(*), SUM(amount), MIN(amount) FROM orders GROUP BY customer_id;`},
	{"join", `CREATE MATERIALIZED VIEW v AS SELECT o.id, o.amount, c.city FROM orders o JOIN customers c ON o.customer_id = c.id;`},
	{"join_aggregate", viewRevenue},
}

func (b *bench) exec(ctx context.Context, sqls ...string) error {
	for _, s := range sqls {
		if _, err := b.db.Exec(ctx, s); err != nil {
			return fmt.Errorf("%.80s: %w", s, err)
		}
	}
	return nil
}

// load empties the tables and loads n orders over 1,000 customers in 20 cities.
func (b *bench) load(ctx context.Context, n int) error {
	if err := cdc.DropSlot(ctx, b.db, b.slot); err != nil {
		return err
	}
	return b.exec(ctx,
		"DROP MATERIALIZED VIEW IF EXISTS revenue_by_city_pg",
		"DELETE FROM orders", "DELETE FROM customers", "DELETE FROM regions", "UPDATE dv_heartbeat SET seq = 0",
		"INSERT INTO customers SELECT g, 'c' || g, 'city' || (g % 20) FROM generate_series(1, 1000) g",
		fmt.Sprintf(`INSERT INTO orders SELECT g, 1 + g %% 1000, (ARRAY['paid','paid','pending','shipped'])[1 + g %% 4],
			(g %% 50000) / 100.0, timestamptz '2026-01-01' + g * interval '1 second' FROM generate_series(1, %d) g`, n),
		"VACUUM ANALYZE orders", "VACUUM ANALYZE customers")
}

// startEngine runs an engine in-process and waits until its snapshot is done.
func (b *bench) startEngine(ctx context.Context, views string, store state.Store, mod func(*engine.Config)) (*engine.Engine, func(), error) {
	cfg := engine.Config{PGURL: b.pgURL, Slot: b.slot, ViewsSQL: views, Store: store, Logger: b.quiet}
	if mod != nil {
		mod(&cfg)
	}
	ctx, cancel := context.WithCancel(ctx)
	e, err := engine.New(ctx, cfg)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	for !e.Ready() {
		select {
		case err := <-done:
			cancel()
			return nil, nil, fmt.Errorf("engine stopped: %w", err)
		case <-time.After(20 * time.Millisecond):
		}
	}
	return e, func() { cancel(); <-done }, nil
}

// syncEngine bumps the heartbeat and waits until the engine applied it.
func (b *bench) syncEngine(ctx context.Context, e *engine.Engine) error {
	b.hb++
	if err := b.exec(ctx, fmt.Sprintf("UPDATE dv_heartbeat SET seq = %d", b.hb)); err != nil {
		return err
	}
	for e.Progress().Heartbeat < b.hb {
		time.Sleep(200 * time.Microsecond)
	}
	return nil
}

// changes applies n single-row updates in transactions of `per` rows.
func (b *bench) changes(ctx context.Context, n, per int, maxID int) error {
	for i := 0; i < n; i += per {
		var stmts []string
		for j := i; j < i+per && j < n; j++ {
			id := 1 + (j*7919)%maxID
			stmts = append(stmts, fmt.Sprintf("UPDATE orders SET amount = amount + 1, status = CASE WHEN status = 'paid' THEN 'pending' ELSE 'paid' END WHERE id = %d", id))
		}
		if err := b.exec(ctx, "BEGIN; "+strings.Join(stmts, "; ")+"; COMMIT"); err != nil {
			return err
		}
	}
	return nil
}

func (b *bench) store(dir string) (state.Store, func(), error) {
	_ = os.RemoveAll(dir)
	p, err := state.OpenPebble(dir)
	if err != nil {
		return nil, nil, err
	}
	return p, func() { p.Close(); os.RemoveAll(dir) }, nil
}

// ---- benchmarks ---------------------------------------------------------

// refreshVsIncremental: after the same changes, how long does REFRESH
// MATERIALIZED VIEW take, and how long does DeltaView take to apply them?
func (b *bench) refreshVsIncremental(ctx context.Context, sizes []int, nChanges int) error {
	w, err := b.csv("refresh_vs_incremental", "rows", "changes", "refresh_ms", "deltaview_ms", "speedup")
	if err != nil {
		return err
	}
	defer w.close()
	for _, n := range sizes {
		if err := b.load(ctx, n); err != nil {
			return err
		}
		if err := b.exec(ctx, strings.Replace(viewRevenue, "revenue_by_city", "revenue_by_city_pg", 1)); err != nil {
			return err
		}
		st, cleanup, err := b.store(filepath.Join(os.TempDir(), "dvbench-refresh"))
		if err != nil {
			return err
		}
		// Initial snapshot, then pause the engine. With no consumer on the
		// slot, the changes below wait in the WAL.
		e, stop, err := b.startEngine(ctx, viewRevenue, st, nil)
		if err != nil {
			return err
		}
		if err := b.syncEngine(ctx, e); err != nil {
			return err
		}
		stop()
		if err := b.changes(ctx, nChanges, 10, n); err != nil {
			return err
		}
		t0 := time.Now()
		if err := b.exec(ctx, "REFRESH MATERIALIZED VIEW revenue_by_city_pg"); err != nil {
			return err
		}
		refresh := time.Since(t0)

		// DeltaView: restart, then time from ready to caught up. That window
		// is applying the pending changes (plus one heartbeat round trip).
		t1 := time.Now()
		e, stop, err = b.startEngine(ctx, viewRevenue, st, nil)
		if err != nil {
			return err
		}
		ready := time.Now()
		if err := b.syncEngine(ctx, e); err != nil {
			return err
		}
		dv := time.Since(ready)
		restart := ready.Sub(t1)
		stop()
		cleanup()
		fmt.Printf("%9d rows: REFRESH %8.1f ms   DeltaView %7.1f ms for %d changes (restart before that %.1f ms)\n",
			n, ms(refresh), ms(dv), nChanges, ms(restart))
		w.row(n, nChanges, ms(refresh), ms(dv), fmt.Sprintf("%.1f", float64(refresh)/float64(dv)))
	}
	return b.exec(ctx, "DROP MATERIALIZED VIEW IF EXISTS revenue_by_city_pg")
}

// latency: Postgres commit time to view commit time, under a steady load.
func (b *bench) latency(ctx context.Context) error {
	if err := b.load(ctx, 100000); err != nil {
		return err
	}
	st, cleanup, err := b.store(filepath.Join(os.TempDir(), "dvbench-latency"))
	if err != nil {
		return err
	}
	defer cleanup()
	var mu sync.Mutex
	var lats []time.Duration
	e, stop, err := b.startEngine(ctx, viewRevenue, st, func(c *engine.Config) {
		c.OnApplied = func(batch []*cdc.TxnBatch, at time.Time) {
			mu.Lock()
			for _, t := range batch {
				lats = append(lats, at.Sub(t.CommitTime))
			}
			mu.Unlock()
		}
	})
	if err != nil {
		return err
	}
	defer stop()
	if err := b.syncEngine(ctx, e); err != nil {
		return err
	}
	mu.Lock()
	lats = lats[:0]
	mu.Unlock()
	const rate, seconds = 200, 10
	tick := time.NewTicker(time.Second / rate)
	for i := 0; i < rate*seconds; i++ {
		<-tick.C
		if err := b.changes(ctx, 1, 1, 100000); err != nil {
			return err
		}
	}
	tick.Stop()
	if err := b.syncEngine(ctx, e); err != nil {
		return err
	}
	mu.Lock()
	defer mu.Unlock()
	sort.Slice(lats, func(i, j int) bool { return lats[i] < lats[j] })
	pct := func(p float64) time.Duration { return lats[int(p*float64(len(lats)-1))] }
	fmt.Printf("%d txns at %d/s: p50 %.2f ms, p99 %.2f ms, max %.2f ms\n", len(lats), rate, ms(pct(0.5)), ms(pct(0.99)), ms(pct(1)))
	w, err := b.csv("latency", "txns", "rate_per_s", "p50_ms", "p90_ms", "p99_ms", "max_ms")
	if err != nil {
		return err
	}
	defer w.close()
	w.row(len(lats), rate, ms(pct(0.5)), ms(pct(0.9)), ms(pct(0.99)), ms(pct(1)))
	return nil
}

// pending stops the engine, writes txns single-row transactions so they
// wait in the slot, then measures how fast a restarted engine applies them.
func (b *bench) pending(ctx context.Context, views string, txns int, mod func(*engine.Config)) (startup, catchUp time.Duration, err error) {
	if err := b.load(ctx, 100000); err != nil {
		return 0, 0, err
	}
	st, cleanup, err := b.store(filepath.Join(os.TempDir(), "dvbench-pending"))
	if err != nil {
		return 0, 0, err
	}
	defer cleanup()
	e, stop, err := b.startEngine(ctx, views, st, mod)
	if err != nil {
		return 0, 0, err
	}
	if err := b.syncEngine(ctx, e); err != nil {
		return 0, 0, err
	}
	stop()
	if err := b.changes(ctx, txns, 1, 100000); err != nil {
		return 0, 0, err
	}
	t0 := time.Now()
	e, stop, err = b.startEngine(ctx, views, st, mod)
	if err != nil {
		return 0, 0, err
	}
	defer stop()
	startup = time.Since(t0)
	if err := b.syncEngine(ctx, e); err != nil {
		return 0, 0, err
	}
	return startup, time.Since(t0), nil
}

// throughput: changes applied per second for each view shape (one core).
func (b *bench) throughput(ctx context.Context, txns int) error {
	w, err := b.csv("throughput", "shape", "txns", "row_changes", "seconds", "txns_per_s", "rows_per_s")
	if err != nil {
		return err
	}
	defer w.close()
	for _, s := range shapes {
		_, catchUp, err := b.pending(ctx, s.sql, txns, nil)
		if err != nil {
			return err
		}
		rows := txns * 2 // each update is -old and +new
		fmt.Printf("%-15s %6d txns in %6.2f s: %8.0f txns/s, %8.0f row changes/s\n", s.name, txns, catchUp.Seconds(),
			float64(txns)/catchUp.Seconds(), float64(rows)/catchUp.Seconds())
		w.row(s.name, txns, rows, fmt.Sprintf("%.3f", catchUp.Seconds()), fmt.Sprintf("%.0f", float64(txns)/catchUp.Seconds()),
			fmt.Sprintf("%.0f", float64(rows)/catchUp.Seconds()))
	}
	return nil
}

// groupCommit: one fsync per transaction versus one per batch of up to 64.
func (b *bench) groupCommit(ctx context.Context, txns int) error {
	w, err := b.csv("group_commit", "mode", "txns", "seconds", "txns_per_s")
	if err != nil {
		return err
	}
	defer w.close()
	for _, m := range []struct {
		name  string
		batch int
	}{{"fsync_per_txn", 1}, {"fsync_per_64", 64}} {
		_, catchUp, err := b.pending(ctx, viewRevenue, txns, func(c *engine.Config) { c.BatchMax = m.batch })
		if err != nil {
			return err
		}
		fmt.Printf("%-14s %6d txns in %6.2f s: %8.0f txns/s\n", m.name, txns, catchUp.Seconds(), float64(txns)/catchUp.Seconds())
		w.row(m.name, txns, fmt.Sprintf("%.3f", catchUp.Seconds()), fmt.Sprintf("%.0f", float64(txns)/catchUp.Seconds()))
	}
	return nil
}

// stateSize: how big the state store gets compared with the source tables.
func (b *bench) stateSize(ctx context.Context, sizes []int) error {
	w, err := b.csv("state_size", "rows", "view", "source_bytes", "state_bytes", "ratio")
	if err != nil {
		return err
	}
	defer w.close()
	for _, n := range sizes {
		if err := b.load(ctx, n); err != nil {
			return err
		}
		var src int64
		if err := b.db.QueryRow(ctx, "SELECT pg_total_relation_size('orders') + pg_total_relation_size('customers')").Scan(&src); err != nil {
			return err
		}
		for _, s := range shapes {
			dir := filepath.Join(os.TempDir(), "dvbench-size")
			_ = os.RemoveAll(dir)
			p, err := state.OpenPebble(dir)
			if err != nil {
				return err
			}
			_, stop, err := b.startEngine(ctx, s.sql, p, nil)
			if err != nil {
				return err
			}
			stop()
			p.Close()
			size, _ := dirSize(dir)
			_ = os.RemoveAll(dir)
			fmt.Printf("%9d rows %-15s source %8.1f MB, state %8.1f MB (%.2fx)\n", n, s.name, mb(src), mb(size), float64(size)/float64(src))
			w.row(n, s.name, src, size, fmt.Sprintf("%.2f", float64(size)/float64(src)))
		}
	}
	return nil
}

// recovery: restart time with N transactions waiting in the slot.
func (b *bench) recovery(ctx context.Context, txns int) error {
	w, err := b.csv("recovery", "pending_txns", "startup_ms", "caught_up_ms")
	if err != nil {
		return err
	}
	defer w.close()
	for _, n := range []int{0, txns / 4, txns} {
		startup, catchUp, err := b.pending(ctx, viewRevenue, n, nil)
		if err != nil {
			return err
		}
		fmt.Printf("%6d pending: ready after %.1f ms, caught up after %.1f ms\n", n, ms(startup), ms(catchUp))
		w.row(n, ms(startup), ms(catchUp))
	}
	return nil
}

// pgIVM compares write throughput on Postgres with a pg_ivm view (triggers
// in the write path) against plain writes (DeltaView reads the WAL later).
func (b *bench) pgIVM(ctx context.Context) error {
	w, err := b.csv("pg_ivm", "mode", "txns", "seconds", "txns_per_s", "note")
	if err != nil {
		return err
	}
	defer w.close()
	var available bool
	if err := b.db.QueryRow(ctx, "SELECT count(*) > 0 FROM pg_available_extensions WHERE name = 'pg_ivm'").Scan(&available); err != nil {
		return err
	}
	if !available {
		fmt.Println("pg_ivm is not installed in this Postgres; skipped (see docs/benchmarks.md)")
		w.row("skipped", 0, 0, 0, "pg_ivm extension not available")
		return nil
	}
	const n = 5000
	if err := b.load(ctx, 100000); err != nil {
		return err
	}
	timeWrites := func() (time.Duration, error) {
		t0 := time.Now()
		err := b.changes(ctx, n, 1, 100000)
		return time.Since(t0), err
	}
	plain, err := timeWrites()
	if err != nil {
		return err
	}
	if err := b.exec(ctx, "CREATE EXTENSION IF NOT EXISTS pg_ivm",
		`SELECT pgivm.create_immv('revenue_ivm', 'SELECT c.city, count(*), sum(o.amount) FROM orders o JOIN customers c ON o.customer_id = c.id WHERE o.status = ''paid'' GROUP BY c.city')`); err != nil {
		return err
	}
	withIVM, err := timeWrites()
	_ = b.exec(ctx, "DROP TABLE IF EXISTS revenue_ivm")
	if err != nil {
		return err
	}
	for _, r := range []struct {
		mode string
		d    time.Duration
		note string
	}{{"plain_writes_deltaview_async", plain, "no trigger; DeltaView applies from the WAL"}, {"pg_ivm_triggers", withIVM, "view maintained inside each write"}} {
		fmt.Printf("%-30s %d txns in %.2f s: %.0f txns/s\n", r.mode, n, r.d.Seconds(), float64(n)/r.d.Seconds())
		w.row(r.mode, n, fmt.Sprintf("%.3f", r.d.Seconds()), fmt.Sprintf("%.0f", float64(n)/r.d.Seconds()), r.note)
	}
	return nil
}

// ---- output -------------------------------------------------------------

type csvFile struct {
	f *os.File
	w *csv.Writer
}

func (b *bench) csv(name string, header ...string) (*csvFile, error) {
	path := filepath.Join(b.out, name+".csv")
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	w := csv.NewWriter(f)
	_ = w.Write([]string{"# " + b.machine + ", " + time.Now().UTC().Format(time.RFC3339)})
	_ = w.Write(header)
	return &csvFile{f: f, w: w}, nil
}

func (c *csvFile) row(vals ...any) {
	rec := make([]string, len(vals))
	for i, v := range vals {
		switch v := v.(type) {
		case float64:
			rec[i] = strconv.FormatFloat(v, 'f', 2, 64)
		default:
			rec[i] = fmt.Sprint(v)
		}
	}
	_ = c.w.Write(rec)
}

func (c *csvFile) close() {
	c.w.Flush()
	_ = c.f.Close()
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
func mb(n int64) float64         { return float64(n) / (1 << 20) }

func dirSize(dir string) (int64, error) {
	var n int64
	err := filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			n += info.Size()
		}
		return nil
	})
	return n, err
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "dvbench:", err)
	os.Exit(1)
}
