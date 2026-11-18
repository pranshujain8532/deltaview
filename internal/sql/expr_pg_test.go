//go:build integration

package sql

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pranshujain8532/deltaview/internal/cdc"
	"github.com/pranshujain8532/deltaview/internal/expr"
	"github.com/pranshujain8532/deltaview/internal/types"
)

// Inputs chosen for edge cases: NULLs, zero, negatives, int4 limits, .5
// rounding, LIKE metacharacters, text that looks like NULL.
const exprRows = `
	(1, 7, 3, 10.50, 3, 'paid', true, 1.5, '2026-01-01', '2026-01-01 10:00:00+00'),
	(2, -7, -2, -0.25, 0.3, 'Pending%', false, -2.5, '1999-12-31', '1999-12-31 23:59:59.5+00'),
	(3, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL),
	(4, 0, 1, 0, 1, '', NULL, 0.5, '2026-11-18', '2026-11-18 00:00:00+00'),
	(5, 2147483647, 9000000000000000000, 123456789.125, 7, 'a_b\c', true, 2.5, '2000-02-29', '2000-02-29 12:00:00+00'),
	(6, -2147483648, -1, 2.5, -2.5, 'NULL', false, 3.5, '2026-06-15', '2026-06-15 06:30:00+00')`

var exprCases = []string{
	// arithmetic and Postgres integer division
	"i + 1", "i - j", "i * 2", "i / 2", "i % 3", "-i", "j / 2", "i / j", "i % j", "j * 3",
	"n + m", "n - 1", "n * m", "n / m", "n / 3", "1 / m", "n % m", "i + n", "j * n", "-n",
	"f + 1", "f * i", "f / 2", "f + n", "abs(i)", "abs(n)", "abs(f)", "round(n)", "round(n, 1)", "round(f)",
	"round(i)", "(i + j) * 2 - 1", "10 / 4", "10.0 / 4", "7 % -3", "-7 % 3", "1e2 + 1",
	"i * i", "j + j", "i + 2147483647",
	// comparisons and three-valued logic
	"i = 7", "i <> 7", "i < j", "n >= 2.5", "s = 'paid'", "s < 'b'", "b = true", "f > n", "i = n",
	"d > '2000-01-01'", "ts >= '2026-01-01 10:00:00'", "d = ts", "NULL = NULL",
	"b AND i > 0", "b OR i > 0", "NOT b", "b AND NULL", "b OR NULL", "NOT (i > 0 AND b)",
	"i IN (7, 0, NULL)", "i NOT IN (7, 0)", "i NOT IN (7, NULL)", "s IN ('paid', 'NULL')",
	"i BETWEEN -10 AND 10", "n NOT BETWEEN 0 AND 3", "i IS NULL", "s IS NOT NULL",
	"b IS TRUE", "b IS NOT TRUE", "b IS FALSE", "b IS UNKNOWN", "(i > 0) IS NOT FALSE",
	"i IS DISTINCT FROM j", "i IS NOT DISTINCT FROM NULL", "n IS DISTINCT FROM 10.5",
	// conditionals
	"COALESCE(i, j, 0)", "COALESCE(n, 0)", "COALESCE(s, 'none')", "NULLIF(i, 7)", "NULLIF(s, '')",
	"CASE WHEN i > 0 THEN 'pos' WHEN i < 0 THEN 'neg' ELSE 'zero' END",
	"CASE i WHEN 7 THEN n ELSE m END", "CASE WHEN b THEN 1 END", "CASE WHEN n > 1 THEN n ELSE i END",
	// text
	"s LIKE 'p%'", "s LIKE '%\\%'", "s LIKE 'a\\_b%'", "s NOT LIKE '_%'", "s LIKE ''",
	"s || '!'", "s || i", "lower(s)", "upper(s)", "length(s)",
	// casts
	"n::int", "n::bigint", "f::int", "f::bigint", "i::numeric", "i::float8", "n::float8", "f::numeric",
	"i::text", "f::text", "b::text", "d::text", "'42'::int + 1", "'1.50'::numeric * 2", "s::text",
	"d::timestamp", "ts::date", "i::smallint", "'t'::boolean", "1::boolean",
}

func TestExpressionsMatchPostgres(t *testing.T) {
	url := os.Getenv("DV_PG_URL")
	if url == "" {
		t.Skip("DV_PG_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(ctx)
	setupSQL := []string{
		"SET TIME ZONE 'UTC'",
		"DROP TABLE IF EXISTS exprt",
		`CREATE TABLE exprt (id int, i int4, j int8, n numeric, m numeric, s text, b bool, f float8, d date, ts timestamptz)`,
		"INSERT INTO exprt VALUES " + exprRows,
	}
	for _, q := range setupSQL {
		if _, err := db.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	tbl, err := cdc.LoadTable(ctx, db, "exprt")
	if err != nil {
		t.Fatal(err)
	}
	cat := func(name string) (*cdc.Table, error) {
		if name == "exprt" {
			return tbl, nil
		}
		return nil, cdc.ErrNoSuchTable
	}
	var rows []types.Row
	if err := cdc.ReadSnapshot(ctx, db, "", []*cdc.Table{tbl}, 100, func(_ string, rs []types.Row) error {
		rows = append(rows, rs...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	for _, c := range exprCases {
		t.Run(c, func(t *testing.T) {
			def, err := ParseSelect("v", "SELECT "+c+" FROM exprt")
			if err != nil {
				t.Fatal(err)
			}
			p, err := Bind(def, cat)
			if err != nil {
				t.Fatalf("bind: %v", err)
			}
			e := p.Root.(*Project).Exprs[0]

			pgVals, pgErr := postgresValues(ctx, db, c)
			ours := make(map[int64]types.Value)
			var ourErr error
			for _, r := range rows {
				v, err := e.Eval(r)
				if err != nil {
					ourErr = err
					continue
				}
				ours[r[0].AsInt()] = v
			}
			if pgErr != nil || ourErr != nil {
				if pgErr == nil || ourErr == nil {
					t.Fatalf("error mismatch: postgres %v, deltaview %v", pgErr, ourErr)
				}
				return
			}
			for id, pgText := range pgVals {
				got := ours[id]
				if pgText == nil {
					if !got.IsNull() {
						t.Errorf("row %d: postgres NULL, deltaview %v", id, got)
					}
					continue
				}
				want, err := types.ParseText(e.Type(), *pgText)
				if err != nil {
					t.Fatalf("row %d: parse postgres %q as %s: %v", id, *pgText, e.Type(), err)
				}
				if got.IsNull() || string(types.Encode(types.Row{got})) != string(types.Encode(types.Row{want})) {
					t.Errorf("row %d: postgres %s, deltaview %v (%s)", id, *pgText, got, e)
				}
			}
		})
	}
}

// postgresValues runs the expression in Postgres and returns its text output
// per row id. Postgres evaluates the whole query or fails it, so one bad row
// makes the whole result an error.
func postgresValues(ctx context.Context, db *pgx.Conn, e string) (map[int64]*string, error) {
	q := fmt.Sprintf("SELECT id, (%s) FROM exprt", e)
	rows, err := db.Query(ctx, q, pgx.QueryResultFormats{pgx.TextFormatCode, pgx.TextFormatCode})
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64]*string)
	for rows.Next() {
		raw := rows.RawValues()
		id, err := strconv.ParseInt(string(raw[0]), 10, 64)
		if err != nil {
			return nil, err
		}
		if raw[1] == nil {
			out[id] = nil
			continue
		}
		s := string(raw[1])
		out[id] = &s
	}
	return out, rows.Err()
}

var _ = expr.IsTrue
