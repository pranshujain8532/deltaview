//go:build integration

package engine

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pranshujain8532/deltaview/internal/cdc"
	"github.com/pranshujain8532/deltaview/internal/state"
	"github.com/pranshujain8532/deltaview/internal/types"
	"github.com/pranshujain8532/deltaview/internal/zset"
)

const testSlot = "engine_it_slot"

const testViews = `
CREATE MATERIALIZED VIEW orders_per_status AS SELECT status, COUNT(*) AS n FROM orders GROUP BY status;
CREATE MATERIALIZED VIEW revenue_by_city AS
  SELECT c.city, COUNT(*) AS orders, SUM(o.amount) AS revenue
  FROM orders o JOIN customers c ON o.customer_id = c.id WHERE o.status = 'paid' GROUP BY c.city;
`

func pg(t *testing.T) *pgx.Conn {
	t.Helper()
	url := os.Getenv("DV_PG_URL")
	if url == "" {
		t.Skip("DV_PG_URL not set")
	}
	db, err := pgx.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	return db
}

func run(t *testing.T, db *pgx.Conn, sqls ...string) {
	t.Helper()
	for _, s := range sqls {
		if _, err := db.Exec(context.Background(), s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

// resetTables gives each test a clean slate. The slot goes first, so the
// deletes are not streamed into the next engine.
func resetTables(t *testing.T, db *pgx.Conn) {
	t.Helper()
	if err := cdc.DropSlot(context.Background(), db, testSlot); err != nil {
		t.Fatal(err)
	}
	run(t, db, "DELETE FROM orders", "DELETE FROM customers", "DELETE FROM regions", "UPDATE dv_heartbeat SET seq = 0",
		"INSERT INTO customers VALUES (1, 'a', 'Delhi'), (2, 'b', 'Mumbai'), (3, 'c', 'Pune')")
	t.Cleanup(func() { _ = cdc.DropSlot(context.Background(), db, testSlot) })
}

type running struct {
	e      *Engine
	cancel context.CancelFunc
	done   chan struct{}
	err    error
}

func start(t *testing.T, store state.Store, mod func(*Config)) *running {
	t.Helper()
	cfg := Config{PGURL: os.Getenv("DV_PG_URL"), Slot: testSlot, ViewsSQL: testViews, Store: store}
	if mod != nil {
		mod(&cfg)
	}
	ctx, cancel := context.WithCancel(context.Background())
	e, err := New(ctx, cfg)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	r := &running{e: e, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(r.done)
		r.err = e.Run(ctx)
	}()
	deadline := time.Now().Add(30 * time.Second)
	for !e.Ready() {
		select {
		case <-r.done:
			t.Fatalf("engine stopped during startup: %v", r.err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("engine not ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(r.stop)
	return r
}

func (r *running) stop() {
	r.cancel()
	<-r.done
}

var hbSeq int64

// sync bumps the heartbeat and waits until the engine has applied it, so
// every earlier transaction is applied too.
func sync_(t *testing.T, db *pgx.Conn, e *Engine) {
	t.Helper()
	hbSeq++
	run(t, db, fmt.Sprintf("UPDATE dv_heartbeat SET seq = %d", hbSeq))
	deadline := time.Now().Add(30 * time.Second)
	for e.Progress().Heartbeat < hbSeq {
		if time.Now().After(deadline) {
			t.Fatalf("engine stuck at heartbeat %d, want %d", e.Progress().Heartbeat, hbSeq)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func randomTxn(r *rand.Rand) string {
	var s []string
	for i := 0; i < 1+r.Intn(3); i++ {
		id := 1 + r.Intn(60)
		switch r.Intn(5) {
		case 0, 1:
			s = append(s, fmt.Sprintf("INSERT INTO orders VALUES (%d, %d, '%s', %d.50, now()) ON CONFLICT (id) DO NOTHING",
				id, 1+r.Intn(4), []string{"paid", "pending"}[r.Intn(2)], r.Intn(100)))
		case 2:
			s = append(s, fmt.Sprintf("UPDATE orders SET status = '%s' WHERE id = %d", []string{"paid", "pending"}[r.Intn(2)], id))
		case 3:
			s = append(s, fmt.Sprintf("DELETE FROM orders WHERE id = %d", id))
		default:
			s = append(s, fmt.Sprintf("UPDATE customers SET city = '%s' WHERE id = %d", []string{"Delhi", "Goa"}[r.Intn(2)], 1+r.Intn(3)))
		}
	}
	return "BEGIN; " + strings.Join(s, "; ") + "; COMMIT"
}

// postgresView renders the result of a query as sorted strings.
func postgresView(t *testing.T, db *pgx.Conn, q string) []string {
	t.Helper()
	rows, err := db.Query(context.Background(), q, pgx.QueryResultFormats{pgx.TextFormatCode})
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var parts []string
		for i, b := range rows.RawValues() {
			typ, _ := types.TypeFromOID(rows.FieldDescriptions()[i].DataTypeOID)
			if b == nil {
				parts = append(parts, "NULL")
				continue
			}
			v, err := types.ParseText(typ, string(b))
			if err != nil {
				t.Fatal(err)
			}
			parts = append(parts, render(v))
		}
		out = append(out, strings.Join(parts, "|"))
	}
	sort.Strings(out)
	return out
}

func render(v types.Value) string {
	if v.Kind() == types.KindInt || v.Kind() == types.KindDecimal {
		return v.AsDecimal().String()
	}
	return v.String()
}

func zsetRows(z *zset.ZSet) []string {
	var out []string
	for _, e := range z.Entries() {
		parts := make([]string, len(e.Row))
		for i, v := range e.Row {
			parts[i] = render(v)
		}
		for i := int64(0); i < e.Weight; i++ {
			out = append(out, strings.Join(parts, "|"))
		}
	}
	sort.Strings(out)
	return out
}

func TestSubscriberEqualsViewUnderLoad(t *testing.T) {
	db := pg(t)
	resetTables(t, db)
	r := start(t, state.NewMemory(), nil)
	sub, err := r.e.Subscribe(context.Background(), "revenue_by_city")
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	local := sub.Rows.Clone()
	last := sub.LSN
	var mu sync.Mutex
	var orderErr error
	go func() {
		for ev := range sub.Events {
			mu.Lock()
			if ev.LSN <= last && orderErr == nil {
				orderErr = fmt.Errorf("event at %d after %d", ev.LSN, last)
			}
			last = ev.LSN
			local.Merge(ev.Delta)
			mu.Unlock()
		}
	}()
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 300; i++ {
		run(t, db, randomTxn(rng))
	}
	sync_(t, db, r.e)
	_, rows, lsn, err := r.e.ReadView("revenue_by_city")
	if err != nil {
		t.Fatal(err)
	}
	want := zsetRows(rows)
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		got := zsetRows(local)
		mu.Unlock()
		if strings.Join(got, ";") == strings.Join(want, ";") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("subscriber diverged at lsn %d:\n got  %v\n want %v", lsn, got, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if orderErr != nil {
		t.Fatal(orderErr)
	}
	pgRows := postgresView(t, db, `SELECT c.city, COUNT(*), SUM(o.amount) FROM orders o JOIN customers c
		ON o.customer_id = c.id WHERE o.status = 'paid' GROUP BY c.city`)
	if strings.Join(pgRows, ";") != strings.Join(want, ";") {
		t.Fatalf("view differs from postgres:\n view %v\n pg   %v", want, pgRows)
	}
}

func TestStalledSubscriberIsDropped(t *testing.T) {
	db := pg(t)
	resetTables(t, db)
	r := start(t, state.NewMemory(), func(c *Config) { c.SubBuffer = 4 })
	stalled, err := r.e.Subscribe(context.Background(), "orders_per_status")
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Now()
	for i := 0; i < 100; i++ {
		run(t, db, fmt.Sprintf("INSERT INTO orders VALUES (%d, 1, 'paid', 1, now())", 1000+i))
	}
	sync_(t, db, r.e) // the pipeline kept going: the stalled client never blocked it
	took := time.Since(t0)
	if !stalled.Resync() {
		t.Fatal("stalled subscriber was not dropped")
	}
	n := 0
	for range stalled.Events { // closed after the buffered events
		n++
	}
	if n > 4 {
		t.Fatalf("stalled subscriber got %d events, buffer is 4", n)
	}
	t.Logf("100 transactions applied in %s with a stalled subscriber", took)
}

// TestCrashAfterCommitBeforeAck stops the engine right after a durable
// commit, before Postgres hears about it. Postgres re-sends the transaction;
// the restarted engine must skip it rather than apply it twice.
func TestCrashAfterCommitBeforeAck(t *testing.T) {
	db := pg(t)
	resetTables(t, db)
	dir := t.TempDir()
	store, err := state.OpenPebble(dir)
	if err != nil {
		t.Fatal(err)
	}
	crashed := make(chan struct{})
	fp := &Failpoints{name: FailAfterCommit, after: 3, exit: func(int) {
		close(crashed)
		runtime.Goexit() // stop the apply loop here, like a process exit
	}}
	r := start(t, store, func(c *Config) { c.Failpoints = fp })
	for i := 0; i < 5; i++ {
		run(t, db, fmt.Sprintf("INSERT INTO orders VALUES (%d, 1, 'paid', 10, now())", i))
	}
	select {
	case <-crashed:
	case <-time.After(10 * time.Second):
		t.Fatal("fail point did not fire")
	}
	r.cancel()
	<-r.done
	committed := r.e.Progress().LSN // progress is published after the ack point; it lags the store
	snap := store.Snapshot()
	stored, _ := state.GetLSN(snap, state.KeyAppliedLSN)
	snap.Close()
	if stored <= committed {
		t.Fatalf("stored LSN %d should be ahead of the acknowledged %d", stored, committed)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store2, err := state.OpenPebble(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	r2 := start(t, store2, nil)
	sync_(t, db, r2.e)
	_, rows, _, err := r2.e.ReadView("orders_per_status")
	if err != nil {
		t.Fatal(err)
	}
	want := postgresView(t, db, "SELECT status, COUNT(*) FROM orders GROUP BY status")
	if got := zsetRows(rows); strings.Join(got, ";") != strings.Join(want, ";") {
		t.Fatalf("after restart: view %v, postgres %v (a re-sent transaction was applied twice?)", got, want)
	}
}

// Restoring an older copy of the state store leaves the slot ahead of the
// stored LSN. Postgres would resume at the slot's position and skip the gap,
// so the engine must take a fresh snapshot instead.
func TestRestoredOlderStateResnapshots(t *testing.T) {
	db := pg(t)
	resetTables(t, db)
	live, backup := t.TempDir(), t.TempDir()
	phase := func(dir string, ids ...int) {
		store, err := state.OpenPebble(dir)
		if err != nil {
			t.Fatal(err)
		}
		r := start(t, store, nil)
		for _, id := range ids {
			run(t, db, fmt.Sprintf("INSERT INTO orders VALUES (%d, 1, 'paid', 1, now())", id))
		}
		sync_(t, db, r.e)
		r.stop()
		store.Close()
	}
	phase(live, 1, 2, 3)
	if err := os.CopyFS(backup+"/s", os.DirFS(live)); err != nil {
		t.Fatal(err)
	}
	phase(live, 4, 5, 6) // the slot moves past the backup

	store, err := state.OpenPebble(backup + "/s")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	r := start(t, store, nil)
	sync_(t, db, r.e)
	_, rows, _, err := r.e.ReadView("orders_per_status")
	if err != nil {
		t.Fatal(err)
	}
	want := postgresView(t, db, "SELECT status, COUNT(*) FROM orders GROUP BY status")
	if got := zsetRows(rows); strings.Join(got, ";") != strings.Join(want, ";") {
		t.Fatalf("restored state served %v, postgres has %v", got, want)
	}
}

func TestUnfollowableTablesAreRejected(t *testing.T) {
	db := pg(t)
	run(t, db, "DROP TABLE IF EXISTS pubcheck_t, part_t",
		"CREATE TABLE pubcheck_t (id int PRIMARY KEY)", "ALTER TABLE pubcheck_t REPLICA IDENTITY FULL",
		"CREATE TABLE part_t (id int) PARTITION BY RANGE (id)")
	t.Cleanup(func() { _, _ = db.Exec(context.Background(), "DROP TABLE IF EXISTS pubcheck_t, part_t") })
	for view, msg := range map[string]string{
		"SELECT id FROM pubcheck_t": "not in publication",
		"SELECT id FROM part_t":     "partitioned",
	} {
		_, err := New(context.Background(), Config{PGURL: os.Getenv("DV_PG_URL"), Slot: testSlot,
			ViewsSQL: "CREATE MATERIALIZED VIEW v AS " + view + ";", Store: state.NewMemory()})
		if err == nil || !strings.Contains(err.Error(), msg) {
			t.Errorf("%s: want error containing %q, got %v", view, msg, err)
		}
	}
}
