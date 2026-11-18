package cdc

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/pranshujain8532/deltaview/internal/types"
)

// ReadSnapshot reads tables inside a transaction pinned to an exported
// snapshot and hands their rows to fn in chunks of at most chunk rows.
// Rows are streamed from the server, so a table is never held in memory.
// Values are read in text format and parsed by the same code as the
// replication stream, so a snapshot row and a streamed row always agree.
func ReadSnapshot(ctx context.Context, conn *pgx.Conn, snapshotName string, tables []*Table,
	chunk int, fn func(table string, rows []types.Row) error) error {
	if chunk <= 0 {
		chunk = 10000
	}
	if _, err := conn.Exec(ctx, "SET TIME ZONE 'UTC'; SET datestyle = 'ISO, MDY'"); err != nil {
		return fmt.Errorf("cdc: snapshot session settings: %w", err)
	}
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return fmt.Errorf("cdc: begin snapshot transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if snapshotName != "" {
		if _, err := tx.Exec(ctx, fmt.Sprintf("SET TRANSACTION SNAPSHOT '%s'", snapshotName)); err != nil {
			return fmt.Errorf("cdc: import snapshot %s: %w", snapshotName, err)
		}
	}
	for _, t := range tables {
		if err := readTable(ctx, tx, t, chunk, fn); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func readTable(ctx context.Context, tx pgx.Tx, t *Table, chunk int, fn func(string, []types.Row) error) error {
	cols := make([]string, len(t.Columns))
	for i, c := range t.Columns {
		if c.Type == types.TUnknown {
			return fmt.Errorf("%w: %s.%s has type oid %d", ErrUnknownType, t.Name, c.Name, c.OID)
		}
		cols[i] = pgx.Identifier{c.Name}.Sanitize()
	}
	q := fmt.Sprintf("SELECT %s FROM %s", strings.Join(cols, ", "),
		pgx.Identifier{t.Namespace, t.Relname}.Sanitize())
	rows, err := tx.Query(ctx, q, pgx.QueryResultFormats{pgx.TextFormatCode})
	if err != nil {
		return fmt.Errorf("cdc: snapshot read %s: %w", t.Name, err)
	}
	defer rows.Close()
	buf := make([]types.Row, 0, chunk)
	for rows.Next() {
		raw := rows.RawValues()
		row := make(types.Row, len(raw))
		for i, b := range raw {
			if b == nil {
				row[i] = types.Null()
				continue
			}
			v, err := types.ParseText(t.Columns[i].Type, string(b))
			if err != nil {
				return fmt.Errorf("cdc: snapshot %s.%s: %w", t.Name, t.Columns[i].Name, err)
			}
			row[i] = v
		}
		buf = append(buf, row)
		if len(buf) == chunk {
			if err := fn(t.Name, buf); err != nil {
				return err
			}
			buf = make([]types.Row, 0, chunk)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("cdc: snapshot read %s: %w", t.Name, err)
	}
	if len(buf) > 0 {
		return fn(t.Name, buf)
	}
	return nil
}
