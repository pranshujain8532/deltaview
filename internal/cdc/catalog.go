package cdc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pglogrepl"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pranshujain8532/deltaview/internal/types"
)

// Table is a source table read from the Postgres catalog. The binder uses its
// columns; the engine stores them to detect schema changes later.
type Table struct {
	Name            string // as used in TxnBatch.Deltas
	Namespace       string
	Relname         string
	Columns         []Column
	ReplicaIdentity byte // 'f' = FULL
}

// ErrNoSuchTable is returned when a view names a table that does not exist.
var ErrNoSuchTable = errors.New("no such table")

// SplitName splits "schema.table" (or "table", meaning public.table).
func SplitName(name string) (namespace, rel string) {
	if i := strings.IndexByte(name, '.'); i >= 0 {
		return name[:i], name[i+1:]
	}
	return "public", name
}

// LoadTable reads a table's columns (in attribute order, which is the order
// pgoutput sends them in) and its replica identity.
func LoadTable(ctx context.Context, conn *pgx.Conn, name string) (*Table, error) {
	ns, rel := SplitName(name)
	t := &Table{Name: QualifiedName(ns, rel), Namespace: ns, Relname: rel}
	var ident string
	err := conn.QueryRow(ctx, `
		SELECT c.relreplident::text FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relname = $2 AND c.relkind IN ('r', 'p')`, ns, rel).Scan(&ident)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrNoSuchTable, t.Name)
	}
	if err != nil {
		return nil, fmt.Errorf("cdc: read catalog for %s: %w", t.Name, err)
	}
	t.ReplicaIdentity = ident[0]
	rows, err := conn.Query(ctx, `
		SELECT a.attname, a.atttypid FROM pg_attribute a
		JOIN pg_class c ON c.oid = a.attrelid JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relname = $2 AND a.attnum > 0 AND NOT a.attisdropped
		  AND a.attgenerated = ''
		ORDER BY a.attnum`, ns, rel)
	if err != nil {
		return nil, fmt.Errorf("cdc: read columns of %s: %w", t.Name, err)
	}
	defer rows.Close()
	for rows.Next() {
		var c Column
		if err := rows.Scan(&c.Name, &c.OID); err != nil {
			return nil, fmt.Errorf("cdc: scan column of %s: %w", t.Name, err)
		}
		c.Type, _ = types.TypeFromOID(c.OID)
		t.Columns = append(t.Columns, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cdc: read columns of %s: %w", t.Name, err)
	}
	return t, nil
}

// SlotInfo is the server's view of a replication slot.
type SlotInfo struct {
	Exists         bool
	Active         bool
	ActivePID      int32
	WALStatus      string // reserved, extended, unreserved, lost
	ConfirmedFlush uint64
}

// GetSlot reads one slot from pg_replication_slots.
func GetSlot(ctx context.Context, conn *pgx.Conn, slot string) (SlotInfo, error) {
	var s SlotInfo
	var flush string
	err := conn.QueryRow(ctx, `
		SELECT active, coalesce(active_pid, 0), coalesce(wal_status, ''),
		       coalesce(confirmed_flush_lsn::text, '0/0')
		FROM pg_replication_slots WHERE slot_name = $1`, slot).
		Scan(&s.Active, &s.ActivePID, &s.WALStatus, &flush)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, nil
	}
	if err != nil {
		return s, fmt.Errorf("cdc: read slot %s: %w", slot, err)
	}
	s.Exists = true
	s.ConfirmedFlush, err = ParseLSN(flush)
	return s, err
}

// SlotLagBytes is how much WAL the slot pins: current WAL position minus the
// position we confirmed. A growing number means Postgres's disk is filling.
func SlotLagBytes(ctx context.Context, conn *pgx.Conn, slot string) (int64, error) {
	var lag int64
	err := conn.QueryRow(ctx, `
		SELECT coalesce(pg_wal_lsn_diff(pg_current_wal_lsn(), confirmed_flush_lsn), 0)::bigint
		FROM pg_replication_slots WHERE slot_name = $1`, slot).Scan(&lag)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("cdc: slot lag: %w", err)
	}
	return lag, nil
}

// DropSlot drops a slot if it exists. A slot still held by a dead client's
// walsender is released first by terminating that backend; DeltaView is the
// only consumer of its slot.
func DropSlot(ctx context.Context, conn *pgx.Conn, slot string) error {
	deadline := time.Now().Add(30 * time.Second)
	for {
		info, err := GetSlot(ctx, conn, slot)
		if err != nil {
			return err
		}
		if !info.Exists {
			return nil
		}
		if info.Active && info.ActivePID != 0 {
			if _, err := conn.Exec(ctx, `SELECT pg_terminate_backend($1)`, info.ActivePID); err != nil {
				return fmt.Errorf("cdc: terminate holder of slot %s: %w", slot, err)
			}
		}
		_, err = conn.Exec(ctx, `SELECT pg_drop_replication_slot($1)`, slot)
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("cdc: drop slot %s: %w", slot, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// ReleaseSlot terminates a walsender still holding the slot after a crash,
// so the restarted engine can attach. It waits until the slot is free.
func ReleaseSlot(ctx context.Context, conn *pgx.Conn, slot string) error {
	deadline := time.Now().Add(30 * time.Second)
	for {
		info, err := GetSlot(ctx, conn, slot)
		if err != nil {
			return err
		}
		if !info.Active {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("cdc: slot %s is still held by pid %d", slot, info.ActivePID)
		}
		if info.ActivePID != 0 {
			_, _ = conn.Exec(ctx, `SELECT pg_terminate_backend($1)`, info.ActivePID)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// CreateSlot creates a logical slot with pgoutput and exports a snapshot.
// The returned snapshot sees exactly the transactions committed before the
// consistent point; the slot delivers exactly those after it. The snapshot
// is valid only while repl stays idle, so the caller must import it before
// running any other command on repl.
func CreateSlot(ctx context.Context, repl *pgconn.PgConn, slot string) (consistentPoint uint64, snapshotName string, err error) {
	res, err := pglogrepl.CreateReplicationSlot(ctx, repl, slot, "pgoutput",
		pglogrepl.CreateReplicationSlotOptions{
			Mode:           pglogrepl.LogicalReplication,
			SnapshotAction: "EXPORT_SNAPSHOT",
		})
	if err != nil {
		return 0, "", fmt.Errorf("cdc: create slot %s: %w", slot, err)
	}
	lsn, err := ParseLSN(res.ConsistentPoint)
	if err != nil {
		return 0, "", fmt.Errorf("cdc: consistent point %q: %w", res.ConsistentPoint, err)
	}
	return lsn, res.SnapshotName, nil
}
