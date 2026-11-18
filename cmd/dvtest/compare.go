package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/pranshujain8532/deltaview/internal/types"
)

// multiset is rows (as canonical strings) with counts. Sets would hide
// duplicate-row bugs, so counts matter.
type multiset map[string]int

// normalize renders one value so equal SQL values compare equal: numbers by
// value rounded to 6 places (the AVG rule, applied to every number), times
// as microseconds, everything else tagged by kind.
func normalize(v types.Value) string {
	switch v.Kind() {
	case types.KindNull:
		return "null"
	case types.KindInt, types.KindDecimal:
		return "n:" + v.AsDecimal().Round(6).String()
	case types.KindFloat:
		f := v.AsFloat()
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return "n:" + types.FormatFloat(f)
		}
		return "n:" + decimal.NewFromFloat(f).Round(6).String()
	case types.KindText:
		return "s:" + strconv.Quote(v.AsText())
	case types.KindBool:
		return "b:" + strconv.FormatBool(v.AsBool())
	case types.KindTimestamp:
		return "t:" + strconv.FormatInt(v.AsInt(), 10)
	case types.KindDate:
		return "d:" + strconv.FormatInt(v.AsInt(), 10)
	}
	return "?"
}

func rowKey(r types.Row) string {
	parts := make([]string, len(r))
	for i, v := range r {
		parts[i] = normalize(v)
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

// postgresRows runs the view's own SELECT on Postgres: the reference answer.
func postgresRows(ctx context.Context, db *pgx.Conn, query string) (multiset, error) {
	rows, err := db.Query(ctx, query, pgx.QueryResultFormats{pgx.TextFormatCode})
	if err != nil {
		return nil, fmt.Errorf("postgres: %w", err)
	}
	defer rows.Close()
	fds := rows.FieldDescriptions()
	colTypes := make([]types.Type, len(fds))
	for i, fd := range fds {
		t, ok := types.TypeFromOID(fd.DataTypeOID)
		if !ok {
			return nil, fmt.Errorf("postgres column %s has unsupported type oid %d", fd.Name, fd.DataTypeOID)
		}
		colTypes[i] = t
	}
	out := multiset{}
	for rows.Next() {
		raw := rows.RawValues()
		r := make(types.Row, len(raw))
		for i, b := range raw {
			if b == nil {
				r[i] = types.Null()
				continue
			}
			if r[i], err = types.ParseText(colTypes[i], string(b)); err != nil {
				return nil, err
			}
		}
		out[rowKey(r)]++
	}
	return out, rows.Err()
}

type viewResponse struct {
	LSN     string `json:"lsn"`
	Columns []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"columns"`
	Rows [][]json.RawMessage `json:"rows"`
}

var typeByName = map[string]types.Type{}

func init() {
	for t := types.TBool; t <= types.TDate; t++ {
		typeByName[t.String()] = t
	}
}

// engineRows reads a view from DeltaView.
func engineRows(ctx context.Context, base, view string) (multiset, string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/views/"+view, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("deltaview: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var b bytes.Buffer
		_, _ = b.ReadFrom(resp.Body)
		return nil, "", fmt.Errorf("deltaview: GET /views/%s: %s %s", view, resp.Status, b.String())
	}
	var vr viewResponse
	if err := json.NewDecoder(resp.Body).Decode(&vr); err != nil {
		return nil, "", fmt.Errorf("deltaview: decode %s: %w", view, err)
	}
	out := multiset{}
	for _, raw := range vr.Rows {
		r := make(types.Row, len(raw))
		for i, cell := range raw {
			t := typeByName[vr.Columns[i].Type]
			v, err := fromJSON(cell, t)
			if err != nil {
				return nil, "", fmt.Errorf("deltaview: %s column %s: %w", view, vr.Columns[i].Name, err)
			}
			r[i] = v
		}
		out[rowKey(r)]++
	}
	return out, vr.LSN, nil
}

func fromJSON(cell json.RawMessage, t types.Type) (types.Value, error) {
	s := string(cell)
	if s == "null" {
		return types.Null(), nil
	}
	switch t.Kind() {
	case types.KindInt, types.KindDecimal:
		d, err := decimal.NewFromString(s)
		return types.Decimal(d), err
	case types.KindFloat:
		if strings.HasPrefix(s, `"`) {
			u, _ := strconv.Unquote(s)
			return types.ParseText(types.TFloat8, u)
		}
		f, err := strconv.ParseFloat(s, 64)
		return types.Float(f), err
	case types.KindBool:
		return types.Bool(s == "true"), nil
	}
	var str string
	if err := json.Unmarshal(cell, &str); err != nil {
		return types.Value{}, err
	}
	switch t.Kind() {
	case types.KindTimestamp:
		tm, err := time.Parse(time.RFC3339Nano, str)
		return types.TimestampFromTime(tm), err
	case types.KindDate:
		return types.ParseText(types.TDate, str)
	}
	return types.Text(str), nil
}

// diff lists rows whose counts differ, as "+row xN" (only in DeltaView) and
// "-row xN" (only in Postgres).
func diff(pg, dv multiset) []string {
	var out []string
	for k, n := range pg {
		if d := dv[k] - n; d < 0 {
			out = append(out, fmt.Sprintf("  missing in deltaview: %s x%d", k, -d))
		}
	}
	for k, n := range dv {
		if d := n - pg[k]; d > 0 {
			out = append(out, fmt.Sprintf("  extra in deltaview:   %s x%d", k, d))
		}
	}
	sort.Strings(out)
	return out
}
