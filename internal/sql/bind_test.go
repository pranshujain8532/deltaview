package sql

import (
	"errors"
	"strings"
	"testing"

	"github.com/pranshujain8532/deltaview/internal/cdc"
	"github.com/pranshujain8532/deltaview/internal/types"
)

// demoCatalog is the demo schema, so planner tests need no database.
func demoCatalog(name string) (*cdc.Table, error) {
	col := func(n string, oid uint32) cdc.Column {
		t, _ := types.TypeFromOID(oid)
		return cdc.Column{Name: n, OID: oid, Type: t}
	}
	switch name {
	case "orders":
		return &cdc.Table{Name: "orders", Namespace: "public", Relname: "orders", ReplicaIdentity: 'f', Columns: []cdc.Column{
			col("id", types.OIDInt8), col("customer_id", types.OIDInt8), col("status", types.OIDText),
			col("amount", types.OIDNumeric), col("created_at", types.OIDTimestamptz)}}, nil
	case "customers":
		return &cdc.Table{Name: "customers", Namespace: "public", Relname: "customers", ReplicaIdentity: 'f', Columns: []cdc.Column{
			col("id", types.OIDInt8), col("name", types.OIDText), col("city", types.OIDText)}}, nil
	case "regions":
		return &cdc.Table{Name: "regions", Namespace: "public", Relname: "regions", ReplicaIdentity: 'f', Columns: []cdc.Column{
			col("id", types.OIDInt8), col("city", types.OIDText), col("region", types.OIDText)}}, nil
	}
	return nil, cdc.ErrNoSuchTable
}

func plan(t *testing.T, query string) *Plan {
	t.Helper()
	def, err := ParseSelect("v", query)
	if err != nil {
		t.Fatalf("parse %q: %v", query, err)
	}
	p, err := Bind(def, demoCatalog)
	if err != nil {
		t.Fatalf("bind %q: %v", query, err)
	}
	return p
}

func TestPlans(t *testing.T) {
	cases := []struct{ query, want string }{
		{"SELECT id, amount FROM orders WHERE status = 'paid'",
			"Project orders.id, orders.amount\n  Filter (orders.status = \"paid\")\n    Scan orders\n"},
		{"SELECT status, COUNT(*) AS n FROM orders GROUP BY status",
			"Project orders.status, agg#0\n  Aggregate group by (orders.status) compute (count(*))\n    Scan orders\n"},
		{"SELECT customer_id, SUM(amount) / COUNT(*) FROM orders GROUP BY 1 HAVING COUNT(*) > 2",
			"Project orders.customer_id, (agg#0 / agg#1::numeric)\n  Filter (agg#1 > 2::bigint)\n" +
				"    Aggregate group by (orders.customer_id) compute (sum(orders.amount), count(*))\n      Scan orders\n"},
		{`SELECT c.city, COUNT(*) AS orders, SUM(o.amount) AS revenue
		  FROM orders o JOIN customers c ON o.customer_id = c.id WHERE o.status = 'paid' GROUP BY c.city`,
			"Project c.city, agg#0, agg#1\n  Aggregate group by (c.city) compute (count(*), sum(o.amount))\n" +
				"    Filter (o.status = \"paid\")\n      Join on (o.customer_id) = (c.id)\n        Scan orders\n        Scan customers\n"},
		{"SELECT DISTINCT status FROM orders", "Distinct\n  Project orders.status\n    Scan orders\n"},
	}
	for _, c := range cases {
		got := Explain(plan(t, c.query).Root)
		if got != c.want {
			t.Errorf("%s\ngot:\n%s\nwant:\n%s", c.query, got, c.want)
		}
	}
}

func TestOutputColumns(t *testing.T) {
	p := plan(t, "SELECT c.city, COUNT(*) AS orders, SUM(o.amount), MAX(o.created_at), AVG(o.id) "+
		"FROM orders o JOIN customers c ON o.customer_id = c.id GROUP BY c.city")
	want := []Column{{"city", types.TText}, {"orders", types.TInt8}, {"sum", types.TNumeric},
		{"max", types.TTimestamptz}, {"avg", types.TNumeric}}
	got := p.Root.Columns()
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("column %d: got %v want %v", i, got[i], want[i])
		}
	}
	if strings.Join(p.Tables, ",") != "orders,customers" {
		t.Errorf("tables %v", p.Tables)
	}
}

func TestJoinSplitsEquiKeysFromResidual(t *testing.T) {
	p := plan(t, "SELECT a.id FROM orders a JOIN orders b ON a.customer_id = b.customer_id AND a.id < b.id")
	want := "Project a.id\n  Filter (a.id < b.id)\n    Join on (a.customer_id) = (b.customer_id)\n      Scan orders\n      Scan orders\n"
	if got := Explain(p.Root); got != want {
		t.Fatalf("got\n%s", got)
	}
}

func TestRejections(t *testing.T) {
	cases := map[string]string{
		"SELECT id FROM orders ORDER BY id":                                       "ORDER BY",
		"SELECT id FROM orders LIMIT 5":                                           "LIMIT",
		"SELECT o.id FROM orders o LEFT JOIN customers c ON o.customer_id = c.id": "outer joins",
		"SELECT id FROM orders WHERE id IN (SELECT id FROM customers)":            "subqueries",
		"SELECT id, now() FROM orders":                                            "now()",
		"SELECT id, random() FROM orders":                                         "random()",
		"SELECT id FROM orders WHERE created_at < CURRENT_TIMESTAMP":              "CURRENT_TIMESTAMP",
		"SELECT id, row_number() OVER () FROM orders":                             "window functions",
		"SELECT id FROM orders UNION SELECT id FROM customers":                    "UNION",
		"SELECT COUNT(DISTINCT status) FROM orders":                               "COUNT(DISTINCT",
		"SELECT string_agg(status, ',') FROM orders":                              "aggregate string_agg",
		"SELECT o.id FROM orders o, customers c WHERE o.customer_id = c.id":       "comma-separated FROM",
		"SELECT o.id FROM orders o JOIN customers c ON o.customer_id > c.id":      "equality",
		"SELECT o.id FROM orders o CROSS JOIN customers c":                        "CROSS JOIN",
		"SELECT DISTINCT ON (status) status FROM orders":                          "DISTINCT ON",
		"WITH x AS (SELECT 1) SELECT id FROM orders":                              "WITH",
		"SELECT 1": "without FROM",
		"SELECT status FROM orders GROUP BY ROLLUP (status)": "ROLLUP",
	}
	for q, feature := range cases {
		def, err := ParseSelect("v", q)
		if err == nil {
			_, err = Bind(def, demoCatalog)
		}
		if err == nil || !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), feature) {
			t.Errorf("%s\n  want unsupported %q, got %v", q, feature, err)
		}
	}
}

func TestBindErrors(t *testing.T) {
	cases := map[string]string{
		"SELECT nope FROM orders": "does not exist",
		"SELECT id FROM orders o JOIN customers c ON o.customer_id = c.id": "ambiguous",
		"SELECT status, amount FROM orders GROUP BY status":                "must appear in the GROUP BY",
		"SELECT id FROM orders WHERE COUNT(*) > 1":                         "not allowed in WHERE",
		"SELECT id FROM orders WHERE amount":                               "must be type boolean",
		"SELECT id FROM orders WHERE status = 1":                           "cannot",
		"SELECT SUM(status) FROM orders":                                   "sum(text) does not exist",
		"SELECT id FROM nosuch":                                            "no such table",
	}
	for q, msg := range cases {
		def, err := ParseSelect("v", q)
		if err == nil {
			_, err = Bind(def, demoCatalog)
		}
		if err == nil || !strings.Contains(err.Error(), msg) {
			t.Errorf("%s\n  want error containing %q, got %v", q, msg, err)
		}
	}
}

func TestParseViewsFile(t *testing.T) {
	defs, err := ParseViews(`
		CREATE MATERIALIZED VIEW a AS SELECT id FROM orders;
		-- a comment
		CREATE VIEW b AS SELECT city, count(*) FROM customers GROUP BY city;`)
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 2 || defs[0].Name != "a" || defs[1].Name != "b" {
		t.Fatalf("got %+v", defs)
	}
	if !strings.HasPrefix(defs[1].Query, "SELECT city, count(*) FROM customers") {
		t.Errorf("deparsed query %q", defs[1].Query)
	}
	if _, err := ParseViews("CREATE TABLE t (id int)"); err == nil {
		t.Error("CREATE TABLE must be rejected")
	}
}
