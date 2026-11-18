// Command dvtest is the differential tester, the judge of DeltaView's
// correctness. It is the only writer to Postgres during a run. It applies
// seeded random transactions, waits until DeltaView has caught up (via the
// heartbeat row), then compares every view with the same SELECT run on
// Postgres, as multisets. If they differ, DeltaView is wrong.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pranshujain8532/deltaview/internal/cdc"
	"github.com/pranshujain8532/deltaview/internal/sql"
)

type options struct {
	pgURL, engine, views, slot string
	seed                       int64
	txns, checkEvery, initial  int
	bulk                       int
	rollbackPct                int
	reset, checkOnly, final    bool
	pause                      time.Duration
	waitTimeout                time.Duration
}

func main() {
	var o options
	flag.StringVar(&o.pgURL, "pg", envOr("DV_PG_URL", "postgres://deltaview:deltaview@localhost:5432/deltaview?sslmode=disable"), "Postgres URL")
	flag.StringVar(&o.engine, "engine", envOr("DV_ENGINE", "http://localhost:8080"), "DeltaView base URL")
	flag.StringVar(&o.views, "views", "views.sql", "views to check")
	flag.StringVar(&o.slot, "slot", "deltaview_slot", "slot dropped by --reset")
	flag.Int64Var(&o.seed, "seed", 1, "random seed; the whole workload follows from it")
	flag.IntVar(&o.txns, "txns", 1000, "transactions to run (0 = until SIGTERM)")
	flag.IntVar(&o.checkEvery, "check-every", 10, "compare all views every N transactions (0 = never)")
	flag.IntVar(&o.initial, "initial", 0, "with --reset: orders to insert before the engine starts")
	flag.IntVar(&o.bulk, "bulk", 0, "with --reset: also load N orders for 40 extra customers (snapshot tests)")
	flag.IntVar(&o.rollbackPct, "rollback-pct", 5, "percent of transactions rolled back")
	flag.BoolVar(&o.reset, "reset", false, "drop the slot, empty the tables, load initial data, then exit")
	flag.BoolVar(&o.checkOnly, "check-only", false, "wait for the engine and compare once")
	flag.BoolVar(&o.final, "final-check", true, "compare once more after the last transaction")
	flag.DurationVar(&o.pause, "pause", 0, "sleep between transactions")
	flag.DurationVar(&o.waitTimeout, "wait-timeout", 3*time.Minute, "how long to wait for the engine to catch up")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	code, err := run(ctx, o)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dvtest:", err)
	}
	os.Exit(code)
}

type tester struct {
	o       options
	db      *pgx.Conn
	views   []sql.ViewDef
	w       *workload
	seq     int64
	history []string // transactions since the last good check
	checks  int
}

func run(ctx context.Context, o options) (int, error) {
	db, err := pgx.Connect(context.Background(), o.pgURL)
	if err != nil {
		return 2, err
	}
	defer db.Close(context.Background())
	if _, err := db.Exec(context.Background(), "SET TIME ZONE 'UTC'"); err != nil {
		return 2, err
	}
	t := &tester{o: o, db: db, w: &workload{r: rand.New(rand.NewSource(o.seed))}}
	if o.reset {
		return t.resetDB()
	}
	src, err := os.ReadFile(o.views)
	if err != nil {
		return 2, err
	}
	if t.views, err = sql.ParseViews(string(src)); err != nil {
		return 2, err
	}
	if err := db.QueryRow(context.Background(), "SELECT seq FROM dv_heartbeat WHERE id = 1").Scan(&t.seq); err != nil {
		return 2, fmt.Errorf("read heartbeat: %w", err)
	}
	if o.checkOnly {
		code, err := t.check(context.Background(), 0)
		if code == 0 {
			fmt.Printf("check passed: %d views equal to postgres at heartbeat %d\n", len(t.views), t.seq)
		}
		return code, err
	}
	start := time.Now()
	n := 0
	for o.txns == 0 || n < o.txns {
		if ctx.Err() != nil {
			break // paused by a signal: finish cleanly, after a whole transaction
		}
		tx := t.w.txn(o.rollbackPct)
		if err := t.exec(tx); err != nil {
			return 2, fmt.Errorf("seed %d txn %d: %s: %w", o.seed, n+1, tx, err)
		}
		n++
		t.history = append(t.history, fmt.Sprintf("#%d %s", n, tx))
		if o.checkEvery > 0 && n%o.checkEvery == 0 {
			if code, err := t.check(context.Background(), n); code != 0 {
				return code, err
			}
		}
		if o.pause > 0 {
			time.Sleep(o.pause)
		}
	}
	if o.final && ctx.Err() == nil {
		if code, err := t.check(context.Background(), n); code != 0 {
			return code, err
		}
	}
	fmt.Printf("seed %d: %d transactions, %d checks of %d views, all equal (%s)\n",
		o.seed, n, t.checks, len(t.views), time.Since(start).Round(time.Millisecond))
	return 0, nil
}

func (t *tester) exec(tx txn) error {
	ctx := context.Background()
	pt, err := t.db.Begin(ctx)
	if err != nil {
		return err
	}
	for _, s := range tx.stmts {
		if _, err := pt.Exec(ctx, s); err != nil {
			_ = pt.Rollback(ctx)
			return err
		}
	}
	if tx.rollback {
		return pt.Rollback(ctx)
	}
	return pt.Commit(ctx)
}

// check bumps the heartbeat, waits until DeltaView reports it, then compares
// every view. Changes arrive in commit order, so once the heartbeat is
// applied every earlier transaction is applied too.
func (t *tester) check(ctx context.Context, n int) (int, error) {
	t.seq++
	if _, err := t.db.Exec(ctx, "UPDATE dv_heartbeat SET seq = $1 WHERE id = 1", t.seq); err != nil {
		return 2, fmt.Errorf("bump heartbeat: %w", err)
	}
	if err := t.waitFor(ctx, t.seq); err != nil {
		return 2, err
	}
	t.checks++
	failed := false
	for _, v := range t.views {
		pg, err := postgresRows(ctx, t.db, v.Query)
		if err != nil {
			return 2, fmt.Errorf("view %s: %w", v.Name, err)
		}
		dv, lsn, err := engineRows(ctx, t.o.engine, v.Name)
		if err != nil {
			return 2, err
		}
		if d := diff(pg, dv); len(d) > 0 {
			if !failed {
				fmt.Printf("MISMATCH seed=%d after transaction %d (check %d, heartbeat %d)\n", t.o.seed, n, t.checks, t.seq)
			}
			failed = true
			fmt.Printf("view %s at lsn %s (%d rows in postgres, %d in deltaview):\n", v.Name, lsn, size(pg), size(dv))
			for _, line := range d {
				fmt.Println(line)
			}
		}
	}
	if failed {
		fmt.Printf("transactions since the last good check (%d):\n", len(t.history))
		for _, h := range t.history {
			fmt.Println("  " + h)
		}
		fmt.Printf("reproduce: make difftest SEED=%d\n", t.o.seed)
		return 1, fmt.Errorf("seed %d: views differ from postgres", t.o.seed)
	}
	t.history = t.history[:0]
	return 0, nil
}

func size(m multiset) int {
	n := 0
	for _, c := range m {
		n += c
	}
	return n
}

type status struct {
	Ready     bool  `json:"ready"`
	Heartbeat int64 `json:"heartbeat"`
}

func (t *tester) waitFor(ctx context.Context, seq int64) error {
	deadline := time.Now().Add(t.o.waitTimeout)
	var last status
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, t.o.engine+"/status", nil)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			err = json.NewDecoder(resp.Body).Decode(&last)
			resp.Body.Close()
			if err == nil && last.Ready && last.Heartbeat >= seq {
				return nil
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	return fmt.Errorf("engine did not reach heartbeat %d within %s (last saw %d, ready=%v)",
		seq, t.o.waitTimeout, last.Heartbeat, last.Ready)
}

// resetDB prepares a clean run. The slot is dropped first so the deletes
// below are never streamed to anyone.
func (t *tester) resetDB() (int, error) {
	ctx := context.Background()
	if err := cdc.DropSlot(ctx, t.db, t.o.slot); err != nil {
		return 2, err
	}
	stmts := []string{"DELETE FROM orders", "DELETE FROM customers", "DELETE FROM regions",
		"UPDATE dv_heartbeat SET seq = 0"}
	stmts = append(stmts, t.w.initialData(t.o.initial)...)
	if t.o.bulk > 0 {
		// Bulk rows use keys and values the random workload never targets, so
		// writes during the snapshot stay small transactions.
		stmts = append(stmts,
			"INSERT INTO customers SELECT 1000 + g, 'Bulk' || g, 'Bulkpur' FROM generate_series(0, 39) g",
			"INSERT INTO regions VALUES (999, 'Bulkpur', 'Central')",
			fmt.Sprintf(`INSERT INTO orders SELECT 100000 + g, 1000 + g %% 40, (ARRAY['archived', 'delivered'])[1 + g %% 2],
				(g %% 50000) / 100.0, timestamptz '2025-01-01' + g * interval '1 second' FROM generate_series(1, %d) g`, t.o.bulk))
	}
	for _, s := range stmts {
		if _, err := t.db.Exec(ctx, s); err != nil {
			return 2, fmt.Errorf("%s: %w", s, err)
		}
	}
	fmt.Printf("reset: tables emptied, %d initial orders loaded (seed %d)\n", t.o.initial, t.o.seed)
	return 0, nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
