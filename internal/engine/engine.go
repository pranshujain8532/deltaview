package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pranshujain8532/deltaview/internal/cdc"
	"github.com/pranshujain8532/deltaview/internal/circuit"
	"github.com/pranshujain8532/deltaview/internal/obs"
	"github.com/pranshujain8532/deltaview/internal/sql"
	"github.com/pranshujain8532/deltaview/internal/state"
	"github.com/pranshujain8532/deltaview/internal/types"
	"github.com/pranshujain8532/deltaview/internal/zset"
)

// Config configures an engine.
type Config struct {
	PGURL       string
	Slot        string // default deltaview_slot
	Publication string // default deltaview_pub
	Heartbeat   string // heartbeat table, default dv_heartbeat
	ViewsSQL    string // contents of views.sql
	Store       state.Store

	QueueSize     int  // bounded queue between reader and apply loop (default 256)
	SnapshotChunk int  // rows per snapshot step (default 10000)
	BatchMax      int  // transactions per state commit, i.e. group commit (default 1)
	NoSync        bool // commit without fsync; only for benchmarks
	Reset         bool // force a fresh snapshot
	SubBuffer     int  // per-subscriber event buffer (default 1024)

	PrintChanges bool // log every transaction's raw changes
	// OnApplied, when set, is called by the apply loop after each durable
	// commit with the transactions it contained. Benchmarks use it.
	OnApplied  func(batch []*cdc.TxnBatch, at time.Time)
	Logger     *slog.Logger
	Metrics    *obs.Metrics
	Tracer     *obs.Tracer
	Failpoints *Failpoints
}

func (c *Config) defaults() {
	if c.Slot == "" {
		c.Slot = "deltaview_slot"
	}
	if c.Publication == "" {
		c.Publication = "deltaview_pub"
	}
	if c.Heartbeat == "" {
		c.Heartbeat = "dv_heartbeat"
	}
	if c.QueueSize <= 0 {
		c.QueueSize = 256
	}
	if c.SnapshotChunk <= 0 {
		c.SnapshotChunk = 10000
	}
	if c.BatchMax <= 0 {
		c.BatchMax = 1
	}
	if c.SubBuffer <= 0 {
		c.SubBuffer = 1024
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.Failpoints == nil {
		c.Failpoints = &Failpoints{}
	}
}

// Engine is one DeltaView process: a CDC reader, an apply loop that owns
// all state, and the circuits of every view.
type Engine struct {
	cfg      Config
	log      *slog.Logger
	store    state.Store
	views    []*circuit.Circuit
	byName   map[string]*circuit.Circuit
	tables   map[string]*cdc.Table // source tables (views and heartbeat)
	hbSeqCol int

	progress atomic.Pointer[Progress]
	acked    atomic.Uint64 // durably committed LSN; the only LSN acknowledged
	slotLag  atomic.Int64
	ready    atomic.Bool
	queue    chan *cdc.TxnBatch
	subReq   chan subRequest
	hub      *hub
	stopped  chan struct{}
}

// Progress is the applied position and heartbeat, always published as a
// pair after a commit, so a heartbeat is never visible before the
// transactions committed ahead of it.
type Progress struct {
	LSN       uint64
	Heartbeat int64
}

// ViewInfo describes a view for clients.
type ViewInfo struct {
	Name    string
	SQL     string
	Columns []circuit.Column
}

// New connects to Postgres, reads table schemas from the catalog and builds
// a circuit for every view. Unsupported SQL fails here, at startup.
func New(ctx context.Context, cfg Config) (*Engine, error) {
	cfg.defaults()
	if cfg.Store == nil {
		return nil, errors.New("engine: no state store")
	}
	e := &Engine{cfg: cfg, log: cfg.Logger, store: cfg.Store, byName: make(map[string]*circuit.Circuit),
		tables: make(map[string]*cdc.Table), queue: make(chan *cdc.TxnBatch, cfg.QueueSize),
		subReq: make(chan subRequest), stopped: make(chan struct{})}
	e.hub = newHub(cfg.SubBuffer, cfg.Metrics)
	e.progress.Store(&Progress{})

	db, err := pgx.Connect(ctx, cfg.PGURL)
	if err != nil {
		return nil, fmt.Errorf("engine: connect: %w", err)
	}
	defer db.Close(context.Background())

	defs, err := sql.ParseViews(cfg.ViewsSQL)
	if err != nil {
		return nil, err
	}
	if len(defs) == 0 {
		return nil, errors.New("engine: views.sql defines no views")
	}
	catalog := func(name string) (*cdc.Table, error) {
		ns, rel := cdc.SplitName(name)
		key := cdc.QualifiedName(ns, rel)
		if t, ok := e.tables[key]; ok {
			return t, nil
		}
		t, err := cdc.LoadTable(ctx, db, name)
		if err != nil {
			return nil, err
		}
		e.tables[t.Name] = t
		return t, nil
	}
	for _, d := range defs {
		p, err := sql.Bind(d, catalog)
		if err != nil {
			return nil, err
		}
		c, err := circuit.Build(p)
		if err != nil {
			return nil, err
		}
		if cfg.Tracer != nil {
			c.Tracer = cfg.Tracer
		}
		e.views = append(e.views, c)
		e.byName[c.View] = c
		e.log.Info("view ready", "view", c.View, "plan", strings.TrimSpace(sql.Explain(p.Root)))
	}
	hb, err := catalog(cfg.Heartbeat)
	if err != nil {
		return nil, fmt.Errorf("engine: heartbeat table: %w", err)
	}
	e.hbSeqCol = -1
	for i, c := range hb.Columns {
		if c.Name == "seq" && c.Type.IsInteger() {
			e.hbSeqCol = i
		}
	}
	if e.hbSeqCol < 0 {
		return nil, fmt.Errorf("engine: %s needs an integer column seq", cfg.Heartbeat)
	}
	srcs := make([]*cdc.Table, 0, len(e.tables))
	for _, t := range e.tables {
		srcs = append(srcs, t)
	}
	if err := cdc.CheckPublication(ctx, db, cfg.Publication, srcs); err != nil {
		return nil, err
	}
	for _, t := range e.tables {
		if t.ReplicaIdentity != 'f' {
			return nil, fmt.Errorf("engine: table %s must use REPLICA IDENTITY FULL "+
				"(ALTER TABLE %s REPLICA IDENTITY FULL): DeltaView subtracts the full old row", t.Name, t.Name)
		}
	}
	return e, nil
}

// Views lists the views in definition order.
func (e *Engine) Views() []ViewInfo {
	out := make([]ViewInfo, len(e.views))
	for i, c := range e.views {
		out[i] = ViewInfo{Name: c.View, SQL: c.SQL, Columns: c.Columns}
	}
	return out
}

// Ready reports whether the initial snapshot is done and views are served.
func (e *Engine) Ready() bool { return e.ready.Load() }

// Progress returns the applied LSN and heartbeat.
func (e *Engine) Progress() Progress { return *e.progress.Load() }

// SlotLag is the last measured slot lag in bytes.
func (e *Engine) SlotLag() int64 { return e.slotLag.Load() }

// QueueDepth is the number of transactions waiting to be applied.
func (e *Engine) QueueDepth() int { return len(e.queue) }

// ErrNoView is returned for an unknown view name.
var ErrNoView = errors.New("no such view")

// ErrNotReady is returned before the initial snapshot completes.
var ErrNotReady = errors.New("engine is not ready (initial snapshot in progress)")

// ReadView reads a view from a store snapshot: every row it returns is valid
// at the one LSN it returns.
func (e *Engine) ReadView(name string) (*ViewInfo, *zset.ZSet, uint64, error) {
	c, ok := e.byName[name]
	if !ok {
		return nil, nil, 0, fmt.Errorf("%w: %s", ErrNoView, name)
	}
	if !e.ready.Load() {
		return nil, nil, 0, ErrNotReady
	}
	snap := e.store.Snapshot()
	defer snap.Close()
	lsn, err := state.GetLSN(snap, state.KeyAppliedLSN)
	if err != nil {
		return nil, nil, 0, err
	}
	rows, err := c.ReadView(snap)
	if err != nil {
		return nil, nil, 0, err
	}
	return &ViewInfo{Name: c.View, SQL: c.SQL, Columns: c.Columns}, rows, lsn, nil
}

// fingerprint covers the view definitions and every source schema. State
// built for a different fingerprint is useless, so a change forces a fresh
// snapshot.
func (e *Engine) fingerprint() string {
	h := sha256.New()
	for _, c := range e.views {
		fmt.Fprintf(h, "view %s\x00%s\x00", c.View, c.SQL)
	}
	names := make([]string, 0, len(e.tables))
	for n := range e.tables {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(h, "table %s", n)
		for _, c := range e.tables[n].Columns {
			fmt.Fprintf(h, " %s:%d:%d:%d", c.Name, c.OID, c.Typmod, c.Attnum)
		}
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Run starts the engine and blocks until ctx is cancelled or a fatal error.
func (e *Engine) Run(ctx context.Context) error {
	defer close(e.stopped)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	db, err := pgx.Connect(ctx, e.cfg.PGURL)
	if err != nil {
		return fmt.Errorf("engine: connect: %w", err)
	}
	defer db.Close(context.Background())

	reason, err := e.needsSnapshot(ctx, db)
	if err != nil {
		return err
	}
	repl, err := cdc.ConnectReplication(ctx, e.cfg.PGURL)
	if err != nil {
		return err
	}
	defer repl.Close(context.Background())

	var start uint64
	if reason != "" {
		e.log.Info("taking initial snapshot", "reason", reason)
		t0 := time.Now()
		if start, err = e.snapshot(ctx, db, repl); err != nil {
			return err
		}
		e.log.Info("snapshot done", "lsn", cdc.FormatLSN(start), "took", time.Since(t0).Round(time.Millisecond))
	} else {
		snap := e.store.Snapshot()
		start, err = state.GetLSN(snap, state.KeyAppliedLSN)
		hb, err2 := state.GetInt(snap, state.KeyHeartbeat)
		snap.Close()
		if err = errors.Join(err, err2); err != nil {
			return err
		}
		e.progress.Store(&Progress{LSN: start, Heartbeat: hb})
		if err := cdc.ReleaseSlot(ctx, db, e.cfg.Slot); err != nil {
			return err
		}
		e.log.Info("resuming", "lsn", cdc.FormatLSN(start), "heartbeat", hb)
	}
	e.acked.Store(start)
	if e.cfg.Metrics != nil {
		e.cfg.Metrics.AppliedLSN.Set(float64(start))
	}
	e.ready.Store(true)

	expect := make(map[string][]cdc.Column)
	for n, t := range e.tables {
		expect[n] = t.Columns
	}
	dec := cdc.NewDecoder(expect, func(name string) bool { _, ok := e.tables[name]; return ok })
	reader := cdc.NewReader(repl, cdc.ReaderConfig{Slot: e.cfg.Slot, Publication: e.cfg.Publication}, dec, &e.acked)
	reader.OnAck = func(uint64) { e.cfg.Failpoints.Hit(FailAfterAck) }

	readerErr := make(chan error, 1)
	go func() { readerErr <- reader.Run(ctx, start, e.queue) }()
	go e.pollSlotLag(ctx)

	err = e.applyLoop(ctx, readerErr)
	cancel()
	e.hub.closeAll()
	if ctx.Err() != nil && errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// needsSnapshot returns why state must be rebuilt, or "" to resume.
func (e *Engine) needsSnapshot(ctx context.Context, db *pgx.Conn) (string, error) {
	if e.cfg.Reset {
		return "reset requested", nil
	}
	snap := e.store.Snapshot()
	_, done, err := snap.Get(state.KeySnapshotDone)
	fp, _, err2 := snap.Get(state.KeyViewsHash)
	applied, err3 := state.GetLSN(snap, state.KeyAppliedLSN)
	snap.Close()
	if err = errors.Join(err, err2, err3); err != nil {
		return "", err
	}
	slot, err := cdc.GetSlot(ctx, db, e.cfg.Slot)
	if err != nil {
		return "", err
	}
	switch {
	case !done:
		return "no completed snapshot in the state store", nil
	case string(fp) != e.fingerprint():
		return "view definitions or table schemas changed", nil
	case !slot.Exists:
		return "replication slot " + e.cfg.Slot + " is missing", nil
	case slot.WALStatus == "lost":
		return "replication slot lost WAL it needed (wal_status = lost)", nil
	case slot.ConfirmedFlush > applied:
		// Postgres would start streaming at the slot's position and silently
		// skip everything between it and our state (a restored backup, or a
		// slot another process advanced). Only a fresh snapshot is correct.
		return fmt.Sprintf("slot confirmed %s but stored state is at %s",
			cdc.FormatLSN(slot.ConfirmedFlush), cdc.FormatLSN(applied)), nil
	}
	return "", nil
}

// snapshot builds every view from scratch with no gap and no double count:
// the exported snapshot sees exactly the commits before the slot's
// consistent point, and the slot streams exactly the commits after it.
func (e *Engine) snapshot(ctx context.Context, db *pgx.Conn, repl *pgconn.PgConn) (uint64, error) {
	e.ready.Store(false)
	if err := e.store.Wipe(); err != nil {
		return 0, err
	}
	if err := cdc.DropSlot(ctx, db, e.cfg.Slot); err != nil {
		return 0, err
	}
	point, snapName, err := cdc.CreateSlot(ctx, repl, e.cfg.Slot)
	if err != nil {
		return 0, err
	}
	// From here repl must stay idle: the next command on it ends the
	// exported snapshot.
	tx := e.store.Begin()
	for _, c := range e.views {
		if _, err := c.Init(ctx, tx); err != nil {
			tx.Discard()
			return 0, err
		}
	}
	if err := tx.Commit(false); err != nil {
		return 0, err
	}

	reader, err := pgx.Connect(ctx, e.cfg.PGURL)
	if err != nil {
		return 0, fmt.Errorf("engine: snapshot connection: %w", err)
	}
	defer reader.Close(context.Background())
	names := make([]string, 0, len(e.tables))
	for n := range e.tables {
		names = append(names, n)
	}
	sort.Strings(names)
	tables := make([]*cdc.Table, len(names))
	for i, n := range names {
		tables[i] = e.tables[n]
	}
	var hb int64
	var rows int
	err = cdc.ReadSnapshot(ctx, reader, snapName, tables, e.cfg.SnapshotChunk, func(table string, chunk []types.Row) error {
		if table == e.cfg.Heartbeat {
			for _, r := range chunk {
				if v := r[e.hbSeqCol]; !v.IsNull() && v.AsInt() > hb {
					hb = v.AsInt()
				}
			}
		}
		delta := zset.New()
		for _, r := range chunk {
			delta.Add(r, 1)
		}
		rows += len(chunk)
		tx := e.store.Begin()
		for _, c := range e.views {
			if !c.Reads(table) {
				continue
			}
			if _, err := c.Step(ctx, tx, map[string]*zset.ZSet{table: delta}); err != nil {
				tx.Discard()
				return err
			}
		}
		return tx.Commit(false)
	})
	if err != nil {
		return 0, fmt.Errorf("engine: snapshot: %w", err)
	}
	tx = e.store.Begin()
	for _, t := range e.tables {
		var cols []string
		for _, c := range t.Columns {
			cols = append(cols, fmt.Sprintf("%s:%d", c.Name, c.OID))
		}
		tx.Set(state.SchemaKey(t.Name), []byte(strings.Join(cols, ",")))
	}
	tx.Set(state.KeyViewsHash, []byte(e.fingerprint()))
	tx.Set(state.KeyAppliedLSN, state.EncodeInt(int64(point)))
	tx.Set(state.KeyHeartbeat, state.EncodeInt(hb))
	tx.Set(state.KeySnapshotDone, []byte{1})
	if err := tx.Commit(true); err != nil {
		return 0, err
	}
	e.progress.Store(&Progress{LSN: point, Heartbeat: hb})
	e.log.Info("snapshot loaded", "rows", rows, "tables", len(tables))
	return point, nil
}

func (e *Engine) pollSlotLag(ctx context.Context) {
	conn, err := pgx.Connect(ctx, e.cfg.PGURL)
	if err != nil {
		e.log.Warn("slot lag poller: connect failed", "err", err)
		return
	}
	defer conn.Close(context.Background())
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		lag, err := cdc.SlotLagBytes(ctx, conn, e.cfg.Slot)
		if err == nil {
			e.slotLag.Store(lag)
			if e.cfg.Metrics != nil {
				e.cfg.Metrics.SlotLagBytes.Set(float64(lag))
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
