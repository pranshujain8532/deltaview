package main

import (
	"fmt"
	"math/rand"
	"strings"
	"time"
)

// workload generates random transactions from one seed. Key spaces are
// small on purpose: collisions, duplicate values, emptied groups and
// re-used join keys are where incremental bugs hide.
type workload struct {
	r *rand.Rand
}

const (
	maxCustomer = 40
	maxOrder    = 300
	maxRegion   = 12
)

var (
	cities   = []string{"'Delhi'", "'Mumbai'", "'Pune'", "'Goa'", "'Agra'", "NULL"}
	regions  = []string{"'North'", "'West'", "'South'", "NULL"}
	statuses = []string{"'paid'", "'paid'", "'pending'", "'shipped'", "'refunded'", "'cancelled'", "NULL"}
	names    = []string{"'Asha'", "'Ravi'", "'Meera'", "'Kabir'", "'Zoya'", "NULL", "''", "'NULL'"}
)

func (w *workload) pick(xs []string) string { return xs[w.r.Intn(len(xs))] }

func (w *workload) amount() string {
	switch w.r.Intn(10) {
	case 0:
		return "NULL"
	case 1, 2:
		// a small set of repeated values: duplicate MIN/MAX and equal rows
		return []string{"10.00", "99.99", "150.50", "0.01"}[w.r.Intn(4)]
	}
	return fmt.Sprintf("%d.%02d", w.r.Intn(300), w.r.Intn(100))
}

func (w *workload) customerRef() string {
	if w.r.Intn(12) == 0 {
		return "NULL"
	}
	return fmt.Sprint(1 + w.r.Intn(maxCustomer+5)) // some point at missing customers
}

func (w *workload) createdAt() string {
	if w.r.Intn(15) == 0 {
		return "NULL"
	}
	t := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(w.r.Int63n(int64(300 * 24 * time.Hour))))
	return "'" + t.Format("2006-01-02 15:04:05.000000") + "+00'"
}

func (w *workload) orderRow(id int) string {
	return fmt.Sprintf("(%d, %s, %s, %s, %s)", id, w.customerRef(), w.pick(statuses), w.amount(), w.createdAt())
}

func (w *workload) orderID() int    { return 1 + w.r.Intn(maxOrder) }
func (w *workload) customerID() int { return 1 + w.r.Intn(maxCustomer) }

// statement returns one random SQL statement.
func (w *workload) statement() string {
	switch w.r.Intn(22) {
	case 0, 1, 2: // multi-row insert of orders
		rows := make([]string, 1+w.r.Intn(5))
		for i := range rows {
			rows[i] = w.orderRow(w.orderID())
		}
		return "INSERT INTO orders VALUES " + strings.Join(rows, ", ") + " ON CONFLICT (id) DO NOTHING"
	case 3: // upsert: becomes an update when the id exists
		return "INSERT INTO orders VALUES " + w.orderRow(w.orderID()) +
			" ON CONFLICT (id) DO UPDATE SET status = EXCLUDED.status, amount = EXCLUDED.amount"
	case 4, 5: // flip in and out of WHERE status = 'paid'
		return fmt.Sprintf("UPDATE orders SET status = %s WHERE id = %d", w.pick(statuses), w.orderID())
	case 6:
		return fmt.Sprintf("UPDATE orders SET amount = %s WHERE id = %d", w.amount(), w.orderID())
	case 7: // join key update, moves rows between groups
		return fmt.Sprintf("UPDATE orders SET customer_id = %s WHERE id = %d", w.customerRef(), w.orderID())
	case 8: // multi-row update
		return fmt.Sprintf("UPDATE orders SET status = %s WHERE customer_id = %d", w.pick(statuses), w.customerID())
	case 9: // multi-row arithmetic update
		return fmt.Sprintf("UPDATE orders SET amount = amount + 1 WHERE status = %s", w.pick(statuses[:6]))
	case 10:
		return fmt.Sprintf("DELETE FROM orders WHERE id = %d", w.orderID())
	case 11: // empties groups
		return fmt.Sprintf("DELETE FROM orders WHERE customer_id = %d", w.customerID())
	case 12: // no-op update: -old +new cancel
		return fmt.Sprintf("UPDATE orders SET status = status WHERE customer_id = %d", w.customerID())
	case 13:
		return "DELETE FROM orders WHERE status = 'refunded'"
	case 14, 15: // customers
		rows := make([]string, 1+w.r.Intn(3))
		for i := range rows {
			rows[i] = fmt.Sprintf("(%d, %s, %s)", w.customerID(), w.pick(names), w.pick(cities))
		}
		return "INSERT INTO customers VALUES " + strings.Join(rows, ", ") + " ON CONFLICT (id) DO NOTHING"
	case 16, 17: // a customer moves city
		return fmt.Sprintf("UPDATE customers SET city = %s WHERE id = %d", w.pick(cities), w.customerID())
	case 18: // customer deleted while orders remain
		return fmt.Sprintf("DELETE FROM customers WHERE id = %d", w.customerID())
	case 19:
		return fmt.Sprintf("UPDATE customers SET name = %s WHERE city = %s", w.pick(names), w.pick(cities[:5]))
	case 20: // regions: duplicate city keys
		return fmt.Sprintf("INSERT INTO regions VALUES (%d, %s, %s) ON CONFLICT (id) DO UPDATE SET city = EXCLUDED.city, region = EXCLUDED.region",
			1+w.r.Intn(maxRegion), w.pick(cities), w.pick(regions))
	default:
		if w.r.Intn(2) == 0 {
			return fmt.Sprintf("DELETE FROM regions WHERE id = %d", 1+w.r.Intn(maxRegion))
		}
		return fmt.Sprintf("UPDATE regions SET region = %s WHERE city = %s", w.pick(regions), w.pick(cities[:5]))
	}
}

// txn is one transaction: 1 to 4 statements, sometimes rolled back.
type txn struct {
	stmts    []string
	rollback bool
}

func (w *workload) txn(rollbackPct int) txn {
	t := txn{rollback: w.r.Intn(100) < rollbackPct}
	n := 1 + w.r.Intn(4)
	if w.r.Intn(10) == 0 {
		n = 1 // also plenty of single-statement transactions
	}
	for i := 0; i < n; i++ {
		t.stmts = append(t.stmts, w.statement())
	}
	return t
}

func (t txn) String() string {
	end := "COMMIT"
	if t.rollback {
		end = "ROLLBACK"
	}
	return "BEGIN; " + strings.Join(t.stmts, "; ") + "; " + end + ";"
}

// initialData fills the tables before the engine starts, so the initial
// snapshot path is part of every run.
func (w *workload) initialData(n int) []string {
	var out []string
	for i := 1; i <= maxCustomer/2; i++ {
		out = append(out, fmt.Sprintf("INSERT INTO customers VALUES (%d, %s, %s)", i, w.pick(names), w.pick(cities)))
	}
	for i := 1; i <= maxRegion/2; i++ {
		out = append(out, fmt.Sprintf("INSERT INTO regions VALUES (%d, %s, %s)", i, w.pick(cities), w.pick(regions)))
	}
	for i := 1; i <= n; i++ {
		out = append(out, "INSERT INTO orders VALUES "+w.orderRow(i))
	}
	return out
}
