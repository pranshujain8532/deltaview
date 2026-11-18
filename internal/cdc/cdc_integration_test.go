//go:build integration

package cdc

import (
	"context"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pranshujain8532/deltaview/internal/types"
	"github.com/shopspring/decimal"
)

const (
	testSlot = "cdct_slot"
	testPub  = "cdct_pub"
)

func pgURL(t *testing.T) string {
	u := os.Getenv("DV_PG_URL")
	if u == "" {
		t.Skip("DV_PG_URL not set")
	}
	return u
}

func dbConn(t *testing.T) *pgx.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := pgx.Connect(ctx, pgURL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(context.Background()) })
	return c
}

func exec(t *testing.T, db *pgx.Conn, sql string) {
	t.Helper()
	if _, err := db.Exec(context.Background(), sql); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// setup recreates the test tables, publication and slot from nothing.
func setup(t *testing.T) *pgx.Conn {
	db := dbConn(t)
	ctx := context.Background()
	if err := DropSlot(ctx, db, testSlot); err != nil {
		t.Fatal(err)
	}
	exec(t, db, `DROP PUBLICATION IF EXISTS `+testPub)
	exec(t, db, `DROP TABLE IF EXISTS cdct_orders, cdct_customers, cdct_keyonly`)
	exec(t, db, `CREATE TABLE cdct_orders (id bigint PRIMARY KEY, status text, amount numeric(12,2),
		created_at timestamptz, body text, score float8, flag bool, day date, small int2, mid int4, label varchar(20))`)
	exec(t, db, `CREATE TABLE cdct_customers (id bigint PRIMARY KEY, city text)`)
	exec(t, db, `CREATE TABLE cdct_keyonly (id bigint PRIMARY KEY, v text)`)
	exec(t, db, `ALTER TABLE cdct_orders REPLICA IDENTITY FULL`)
	exec(t, db, `ALTER TABLE cdct_customers REPLICA IDENTITY FULL`)
	exec(t, db, `CREATE PUBLICATION `+testPub+` FOR TABLE cdct_orders, cdct_customers, cdct_keyonly`)
	t.Cleanup(func() {
		ctx := context.Background()
		_ = DropSlot(ctx, db, testSlot)
		_, _ = db.Exec(ctx, `DROP PUBLICATION IF EXISTS `+testPub)
	})
	return db
}

type stream struct {
	t      *testing.T
	out    chan *TxnBatch
	errc   chan error
	cancel context.CancelFunc
	done   chan struct{}
	acked  atomic.Uint64
}

// open starts a reader. When create is true it creates the slot first and
// returns its consistent point as the start position.
func open(t *testing.T, from uint64, create bool) (*stream, uint64) {
	return openURL(t, pgURL(t), from, create)
}

func openURL(t *testing.T, url string, from uint64, create bool) (*stream, uint64) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	repl, err := ConnectReplication(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	if create {
		from, _, err = CreateSlot(ctx, repl, testSlot)
		if err != nil {
			t.Fatal(err)
		}
	}
	s := &stream{t: t, out: make(chan *TxnBatch, 16), errc: make(chan error, 1), cancel: cancel, done: make(chan struct{})}
	s.acked.Store(from)
	r := NewReader(repl, ReaderConfig{Slot: testSlot, Publication: testPub}, NewDecoder(nil, nil), &s.acked)
	go func() {
		s.errc <- r.Run(ctx, from, s.out)
		repl.Close(context.Background())
		close(s.done)
	}()
	t.Cleanup(s.stop)
	return s, from
}

func (s *stream) stop() {
	s.cancel()
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
	}
}

// next returns the next batch and acknowledges it, as the M1 engine does.
func (s *stream) next() *TxnBatch {
	s.t.Helper()
	select {
	case b := <-s.out:
		s.acked.Store(b.EndLSN)
		return b
	case err := <-s.errc:
		s.t.Fatalf("reader stopped: %v", err)
	case <-time.After(10 * time.Second):
		s.t.Fatal("timed out waiting for a transaction")
	}
	return nil
}

func (s *stream) expectError() error {
	s.t.Helper()
	select {
	case b := <-s.out:
		s.t.Fatalf("expected an error, got batch %v", b.Deltas)
	case err := <-s.errc:
		return err
	case <-time.After(10 * time.Second):
		s.t.Fatal("timed out waiting for an error")
	}
	return nil
}

func dec(s string) types.Value { return types.Decimal(decimal.RequireFromString(s)) }

func TestMultiRowInsertAndTypes(t *testing.T) {
	db := setup(t)
	s, _ := open(t, 0, true)
	exec(t, db, `INSERT INTO cdct_orders VALUES
		(1, 'paid', 100.50, '2026-11-18 10:00:00.123456+00', 'x', 1.5, true, '2026-11-18', 7, 70000, 'v'),
		(2, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL),
		(3, 'NULL', 0.10, '2026-11-18 15:30:00+05:30', '', -0.25, false, '1999-01-01', -1, -2, '')`)
	b := s.next()
	z := b.Deltas["cdct_orders"]
	if z.Len() != 3 || b.Changes != 3 {
		t.Fatalf("want 3 rows, got %v", z)
	}
	want1 := types.Row{types.Int(1), types.Text("paid"), dec("100.5"), types.Timestamp(1794996000123456),
		types.Text("x"), types.Float(1.5), types.Bool(true), types.Date(20775), types.Int(7), types.Int(70000), types.Text("v")}
	if z.Weight(want1) != 1 {
		t.Errorf("row 1 decoded wrongly: %v", z)
	}
	nulls := types.Row{types.Int(2)}
	for i := 0; i < 10; i++ {
		nulls = append(nulls, types.Null())
	}
	if z.Weight(nulls) != 1 {
		t.Errorf("NULL row decoded wrongly: %v", z)
	}
	want3 := types.Row{types.Int(3), types.Text("NULL"), dec("0.1"), types.Timestamp(1794996000000000),
		types.Text(""), types.Float(-0.25), types.Bool(false), types.Date(10592), types.Int(-1), types.Int(-2), types.Text("")}
	if z.Weight(want3) != 1 {
		t.Errorf("row 3 decoded wrongly (text 'NULL' must not be NULL): %v", z)
	}
}

func TestUpdateAndDelete(t *testing.T) {
	db := setup(t)
	exec(t, db, `INSERT INTO cdct_orders (id, status, amount) VALUES (1, 'pending', 50)`)
	s, _ := open(t, 0, true)
	exec(t, db, `UPDATE cdct_orders SET status = 'paid', amount = 70 WHERE id = 1`)
	b := s.next()
	z := b.Deltas["cdct_orders"]
	old := types.Row{types.Int(1), types.Text("pending"), dec("50")}
	nw := types.Row{types.Int(1), types.Text("paid"), dec("70")}
	for i := 0; i < 8; i++ {
		old = append(old, types.Null())
		nw = append(nw, types.Null())
	}
	if z.Len() != 2 || z.Weight(old) != -1 || z.Weight(nw) != 1 {
		t.Fatalf("update: %v", z)
	}
	exec(t, db, `DELETE FROM cdct_orders WHERE id = 1`)
	z = s.next().Deltas["cdct_orders"]
	if z.Len() != 1 || z.Weight(nw) != -1 {
		t.Fatalf("delete: %v", z)
	}
}

func TestLargeUnchangedToastValue(t *testing.T) {
	db := setup(t)
	// ~100 KB of md5 text does not compress below the TOAST threshold, so
	// it is stored out of line and an UPDATE that skips it sends 'u'.
	exec(t, db, `INSERT INTO cdct_orders (id, status, body)
		SELECT 1, 'pending', string_agg(md5(i::text), '') FROM generate_series(1, 3200) i`)
	var body string
	if err := db.QueryRow(context.Background(), `SELECT body FROM cdct_orders WHERE id = 1`).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if len(body) < 100000 {
		t.Fatalf("body too small: %d", len(body))
	}
	s, _ := open(t, 0, true)
	exec(t, db, `UPDATE cdct_orders SET status = 'paid' WHERE id = 1`)
	z := s.next().Deltas["cdct_orders"]
	if z.Len() != 2 {
		t.Fatalf("want -old +new, got %d entries", z.Len())
	}
	for _, e := range z.Entries() {
		if got := e.Row[4]; got.IsNull() || got.AsText() != body {
			t.Fatalf("body lost in %+d row (null=%v)", e.Weight, got.IsNull())
		}
	}
}

func TestTwoTablesOneTransaction(t *testing.T) {
	db := setup(t)
	s, _ := open(t, 0, true)
	exec(t, db, `BEGIN; INSERT INTO cdct_customers VALUES (7, 'Delhi');
		INSERT INTO cdct_orders (id, status) VALUES (1, 'paid'); COMMIT`)
	b := s.next()
	if b.Deltas["cdct_customers"].Len() != 1 || b.Deltas["cdct_orders"].Len() != 1 {
		t.Fatalf("want both tables in one batch: %v", b.Deltas)
	}
}

func TestRolledBackTransactionProducesNothing(t *testing.T) {
	db := setup(t)
	s, _ := open(t, 0, true)
	exec(t, db, `BEGIN; INSERT INTO cdct_customers VALUES (1, 'Ghost'); ROLLBACK`)
	exec(t, db, `INSERT INTO cdct_customers VALUES (2, 'Real')`)
	b := s.next()
	z := b.Deltas["cdct_customers"]
	if z.Len() != 1 || z.Weight(types.Row{types.Int(2), types.Text("Real")}) != 1 {
		t.Fatalf("rolled back row leaked or real row missing: %v", z)
	}
}

func TestRestartResumesWithoutLoss(t *testing.T) {
	db := setup(t)
	s, _ := open(t, 0, true)
	exec(t, db, `INSERT INTO cdct_customers VALUES (1, 'A')`)
	first := s.next()
	// Give the reader time to send the ack, then stop it.
	time.Sleep(300 * time.Millisecond)
	s.stop()
	exec(t, db, `INSERT INTO cdct_customers VALUES (2, 'B')`)
	exec(t, db, `INSERT INTO cdct_customers VALUES (3, 'C')`)
	if err := ReleaseSlot(context.Background(), db, testSlot); err != nil {
		t.Fatal(err)
	}
	s2, _ := open(t, first.EndLSN, false)
	var seen []int64
	for len(seen) < 2 {
		b := s2.next()
		if b.EndLSN <= first.EndLSN {
			continue // a re-sent duplicate; the engine skips these
		}
		for _, e := range b.Deltas["cdct_customers"].Entries() {
			seen = append(seen, e.Row[0].AsInt())
		}
	}
	if seen[0] != 2 || seen[1] != 3 {
		t.Fatalf("want ids 2, 3 after restart, got %v", seen)
	}
}

func TestTruncateStopsTheStream(t *testing.T) {
	db := setup(t)
	s, _ := open(t, 0, true)
	exec(t, db, `TRUNCATE cdct_customers`)
	if err := s.expectError(); err == nil || !strings.Contains(err.Error(), "TRUNCATE") {
		t.Fatalf("want truncate error, got %v", err)
	}
}

func TestDefaultReplicaIdentityIsRejected(t *testing.T) {
	db := setup(t)
	exec(t, db, `INSERT INTO cdct_keyonly VALUES (1, 'a')`)
	s, _ := open(t, 0, true)
	exec(t, db, `UPDATE cdct_keyonly SET v = 'b' WHERE id = 1`)
	if err := s.expectError(); err == nil || !strings.Contains(err.Error(), "REPLICA IDENTITY FULL") {
		t.Fatalf("want replica identity error, got %v", err)
	}
}

func TestSnapshotMatchesStreamFormat(t *testing.T) {
	db := setup(t)
	exec(t, db, `INSERT INTO cdct_orders VALUES
		(1, 'paid', 100.50, '2026-11-18 10:00:00.123456+00', 'x', 1.5, true, '2026-11-18', 7, 70000, 'v')`)
	ctx := context.Background()
	repl, err := ConnectReplication(ctx, pgURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer repl.Close(ctx)
	_, snap, err := CreateSlot(ctx, repl, testSlot)
	if err != nil {
		t.Fatal(err)
	}
	// Committed after the slot: must not be visible in the snapshot.
	exec(t, db, `INSERT INTO cdct_orders (id) VALUES (2)`)
	tbl, err := LoadTable(ctx, db, "cdct_orders")
	if err != nil {
		t.Fatal(err)
	}
	reader := dbConn(t)
	var got []types.Row
	err = ReadSnapshot(ctx, reader, snap, []*Table{tbl}, 1, func(_ string, rows []types.Row) error {
		got = append(got, rows...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("snapshot should see only the row committed before the slot, got %v", got)
	}
	want := types.Row{types.Int(1), types.Text("paid"), dec("100.5"), types.Timestamp(1794996000123456),
		types.Text("x"), types.Float(1.5), types.Bool(true), types.Date(20775), types.Int(7), types.Int(70000), types.Text("v")}
	if string(types.Encode(got[0])) != string(types.Encode(want)) {
		t.Fatalf("snapshot row %v != %v", got[0], want)
	}
}

func TestQuietStreamIsKeptAlive(t *testing.T) {
	db := setup(t)
	// A 1 s sender timeout: without status updates the server would drop us.
	s, _ := openURL(t, pgURL(t)+"&wal_sender_timeout=1000", 0, true)
	time.Sleep(4 * time.Second)
	exec(t, db, `INSERT INTO cdct_customers VALUES (1, 'still here')`)
	if b := s.next(); b.Deltas["cdct_customers"].Len() != 1 {
		t.Fatalf("unexpected batch %v", b.Deltas)
	}
}
