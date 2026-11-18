package cdc

import (
	"errors"
	"testing"

	"github.com/jackc/pglogrepl"

	"github.com/pranshujain8532/deltaview/internal/types"
	"github.com/pranshujain8532/deltaview/internal/zset"
	"github.com/shopspring/decimal"
)

func relMsg(id uint32, name string, cols ...[2]any) *pglogrepl.RelationMessage {
	m := &pglogrepl.RelationMessage{RelationID: id, Namespace: "public", RelationName: name}
	for _, c := range cols {
		m.Columns = append(m.Columns, &pglogrepl.RelationMessageColumn{Name: c[0].(string), DataType: uint32(c[1].(int))})
	}
	m.ColumnNum = uint16(len(m.Columns))
	return m
}

func tup(vals ...any) *pglogrepl.TupleData {
	t := &pglogrepl.TupleData{}
	for _, v := range vals {
		c := &pglogrepl.TupleDataColumn{}
		switch v := v.(type) {
		case nil:
			c.DataType = pglogrepl.TupleDataTypeNull
		case toast:
			c.DataType = pglogrepl.TupleDataTypeToast
		case string:
			c.DataType = pglogrepl.TupleDataTypeText
			c.Data = []byte(v)
		}
		t.Columns = append(t.Columns, c)
	}
	t.ColumnNum = uint16(len(t.Columns))
	return t
}

type toast struct{}

func ordersRel() *pglogrepl.RelationMessage {
	return relMsg(1, "orders", [2]any{"id", types.OIDInt8}, [2]any{"status", types.OIDText},
		[2]any{"amount", types.OIDNumeric})
}

func run(t *testing.T, d *Decoder, msgs ...pglogrepl.Message) *TxnBatch {
	t.Helper()
	var out *TxnBatch
	for _, m := range msgs {
		b, err := d.Handle(m)
		if err != nil {
			t.Fatalf("handle %T: %v", m, err)
		}
		if b != nil {
			out = b
		}
	}
	return out
}

func row(id int64, status string, amount string) types.Row {
	r := types.Row{types.Int(id), types.Text(status), types.Null()}
	if amount != "" {
		r[2] = types.Decimal(decimal.RequireFromString(amount))
	}
	return r
}

func TestInsertUpdateDelete(t *testing.T) {
	d := NewDecoder(nil, nil)
	b := run(t, d, ordersRel(),
		&pglogrepl.BeginMessage{Xid: 7},
		&pglogrepl.InsertMessage{RelationID: 1, Tuple: tup("1", "paid", "100.00")},
		&pglogrepl.UpdateMessage{RelationID: 1, OldTupleType: 'O', OldTuple: tup("2", "paid", "50"), NewTuple: tup("2", "paid", "70")},
		&pglogrepl.DeleteMessage{RelationID: 1, OldTupleType: 'O', OldTuple: tup("3", "pending", nil)},
		&pglogrepl.CommitMessage{TransactionEndLSN: 0x1234},
	)
	if b == nil || b.EndLSN != 0x1234 || b.Changes != 3 {
		t.Fatalf("bad batch %+v", b)
	}
	want := zset.New()
	want.Add(row(1, "paid", "100"), 1)
	want.Add(row(2, "paid", "50"), -1)
	want.Add(row(2, "paid", "70"), 1)
	want.Add(row(3, "pending", ""), -1)
	if got := b.Deltas["orders"]; !got.Equal(want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
}

func TestUnchangedToastCopiedFromOldRow(t *testing.T) {
	d := NewDecoder(nil, nil)
	b := run(t, d, ordersRel(), &pglogrepl.BeginMessage{},
		&pglogrepl.UpdateMessage{RelationID: 1, OldTupleType: 'O',
			OldTuple: tup("1", "a-very-large-value", "1"), NewTuple: tup("1", toast{}, "2")},
		&pglogrepl.CommitMessage{TransactionEndLSN: 1})
	if w := b.Deltas["orders"].Weight(row(1, "a-very-large-value", "2")); w != 1 {
		t.Fatalf("toast value not copied: %v", b.Deltas["orders"])
	}
}

func TestUpdateWithoutOldTupleFails(t *testing.T) {
	d := NewDecoder(nil, nil)
	run(t, d, ordersRel(), &pglogrepl.BeginMessage{})
	for _, m := range []pglogrepl.Message{
		&pglogrepl.UpdateMessage{RelationID: 1, NewTuple: tup("1", "x", "1")},
		&pglogrepl.UpdateMessage{RelationID: 1, OldTupleType: 'K', OldTuple: tup("1", nil, nil), NewTuple: tup("1", "x", "1")},
		&pglogrepl.DeleteMessage{RelationID: 1, OldTupleType: 'K', OldTuple: tup("1", nil, nil)},
	} {
		if _, err := d.Handle(m); !errors.Is(err, ErrNoOldTuple) {
			t.Errorf("%T: want ErrNoOldTuple, got %v", m, err)
		}
	}
}

func TestTruncateFails(t *testing.T) {
	d := NewDecoder(nil, nil)
	run(t, d, ordersRel(), &pglogrepl.BeginMessage{})
	if _, err := d.Handle(&pglogrepl.TruncateMessage{}); !errors.Is(err, ErrTruncate) {
		t.Fatalf("want ErrTruncate, got %v", err)
	}
}

func TestUnknownTypeFails(t *testing.T) {
	d := NewDecoder(nil, nil)
	run(t, d, relMsg(2, "docs", [2]any{"id", types.OIDInt4}, [2]any{"body", 3802 /* jsonb */}),
		&pglogrepl.BeginMessage{})
	if _, err := d.Handle(&pglogrepl.InsertMessage{RelationID: 2, Tuple: tup("1", "{}")}); !errors.Is(err, ErrUnknownType) {
		t.Fatalf("want ErrUnknownType, got %v", err)
	}
}

func TestSchemaChangeFails(t *testing.T) {
	expect := map[string][]Column{"orders": {
		{Name: "id", OID: types.OIDInt8}, {Name: "status", OID: types.OIDText}, {Name: "amount", OID: types.OIDNumeric},
	}}
	d := NewDecoder(expect, nil)
	if _, err := d.Handle(ordersRel()); err != nil {
		t.Fatalf("matching schema rejected: %v", err)
	}
	added := relMsg(1, "orders", [2]any{"id", types.OIDInt8}, [2]any{"status", types.OIDText},
		[2]any{"amount", types.OIDNumeric}, [2]any{"note", types.OIDText})
	if _, err := d.Handle(added); !errors.Is(err, ErrSchemaChanged) {
		t.Fatalf("want ErrSchemaChanged, got %v", err)
	}
}

func TestTwoTablesOneTxnAndFilter(t *testing.T) {
	d := NewDecoder(nil, func(name string) bool { return name != "ignored" })
	b := run(t, d, ordersRel(),
		relMsg(2, "customers", [2]any{"id", types.OIDInt8}, [2]any{"city", types.OIDText}),
		relMsg(3, "ignored", [2]any{"id", types.OIDInt8}),
		&pglogrepl.BeginMessage{},
		&pglogrepl.InsertMessage{RelationID: 1, Tuple: tup("1", "paid", "1")},
		&pglogrepl.InsertMessage{RelationID: 2, Tuple: tup("7", "Delhi")},
		&pglogrepl.InsertMessage{RelationID: 3, Tuple: tup("9")},
		&pglogrepl.CommitMessage{TransactionEndLSN: 9})
	if len(b.Deltas) != 2 || b.Deltas["orders"].Len() != 1 || b.Deltas["customers"].Len() != 1 {
		t.Fatalf("bad deltas %v", b.Deltas)
	}
}

func TestEmptyTransactionStillYieldsBatch(t *testing.T) {
	// The batch must still carry its LSN so the applied position advances.
	d := NewDecoder(nil, nil)
	b := run(t, d, &pglogrepl.BeginMessage{}, &pglogrepl.CommitMessage{TransactionEndLSN: 42})
	if b == nil || b.EndLSN != 42 || len(b.Deltas) != 0 {
		t.Fatalf("bad batch %+v", b)
	}
}

func TestProtocolOrder(t *testing.T) {
	d := NewDecoder(nil, nil)
	if _, err := d.Handle(&pglogrepl.CommitMessage{}); !errors.Is(err, ErrProtocol) {
		t.Fatalf("commit without begin: %v", err)
	}
	run(t, d, &pglogrepl.BeginMessage{})
	if _, err := d.Handle(&pglogrepl.BeginMessage{}); !errors.Is(err, ErrProtocol) {
		t.Fatalf("nested begin: %v", err)
	}
}

func TestTruncateOfUnwantedTableIsIgnored(t *testing.T) {
	d := NewDecoder(nil, func(name string) bool { return name == "orders" })
	run(t, d, ordersRel(), relMsg(5, "audit_log", [2]any{"id", types.OIDInt8}), &pglogrepl.BeginMessage{})
	if _, err := d.Handle(&pglogrepl.TruncateMessage{RelationIDs: []uint32{5}}); err != nil {
		t.Fatalf("truncate of a table no view reads must not stop the stream: %v", err)
	}
	if _, err := d.Handle(&pglogrepl.TruncateMessage{RelationIDs: []uint32{5, 1}}); !errors.Is(err, ErrTruncate) {
		t.Fatalf("truncate of orders must stop the stream, got %v", err)
	}
}

func TestTypmodChangeIsASchemaChange(t *testing.T) {
	expect := map[string][]Column{"orders": {
		{Name: "id", OID: types.OIDInt8, Typmod: -1}, {Name: "status", OID: types.OIDText, Typmod: -1},
		{Name: "amount", OID: types.OIDNumeric, Typmod: 786438}, // numeric(12,2)
	}}
	m := ordersRel()
	for _, c := range m.Columns {
		c.TypeModifier = -1
	}
	m.Columns[2].TypeModifier = 917510 // numeric(14,2)
	if _, err := NewDecoder(expect, nil).Handle(m); !errors.Is(err, ErrSchemaChanged) {
		t.Fatalf("want ErrSchemaChanged, got %v", err)
	}
}
