package cdc

import (
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pglogrepl"

	"github.com/pranshujain8532/deltaview/internal/types"
	"github.com/pranshujain8532/deltaview/internal/zset"
)

// Errors the decoder stops on. Each one means "continuing would produce a
// wrong view", so the engine must halt rather than guess.
var (
	ErrTruncate      = errors.New("cdc: TRUNCATE is not supported; reset DeltaView after truncating a table")
	ErrNoOldTuple    = errors.New("cdc: change without the full old row; the table needs REPLICA IDENTITY FULL")
	ErrUnknownType   = errors.New("cdc: unsupported column type")
	ErrSchemaChanged = errors.New("cdc: table schema changed; online schema changes are not supported, reset DeltaView")
	ErrProtocol      = errors.New("cdc: unexpected message order")
)

// TxnBatch is one committed Postgres transaction turned into Z-set deltas.
// Views apply a whole batch or nothing, so they never show half a transaction.
type TxnBatch struct {
	EndLSN     uint64 // end of this transaction in the WAL; what we acknowledge
	CommitLSN  uint64
	CommitTime time.Time
	Xid        uint32
	Deltas     map[string]*zset.ZSet // table name -> delta
	Changes    int                   // row-level changes received (before cancellation)
}

// Column is one column of a replicated table.
type Column struct {
	Name   string
	OID    uint32
	Typmod int32      // atttypmod, e.g. the precision of numeric(12,2)
	Attnum int16      // catalog only; pgoutput does not send it
	Type   types.Type // TUnknown when OID is not supported
}

// Relation is a table as announced by a pgoutput Relation message.
type Relation struct {
	ID      uint32
	Name    string // "orders" for the public schema, "schema.table" otherwise
	Columns []Column
}

// QualifiedName builds the table name used everywhere in DeltaView.
func QualifiedName(namespace, name string) string {
	if namespace == "" || namespace == "public" {
		return name
	}
	return namespace + "." + name
}

// SameColumns reports whether two column lists have the same names and types.
func SameColumns(a, b []Column) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name || a[i].OID != b[i].OID || a[i].Typmod != b[i].Typmod {
			return false
		}
	}
	return true
}

// Decoder is the transaction assembler. It turns the pgoutput message stream
// into TxnBatches. It holds no connection, so it is tested without Postgres.
type Decoder struct {
	rels   map[uint32]*Relation
	cur    *TxnBatch
	expect map[string][]Column // schemas the views were bound against
	want   func(table string) bool
}

// NewDecoder returns a decoder. expect maps table names to the column lists
// the views were built against; a Relation message that disagrees stops the
// stream. want selects which tables to decode (nil means all).
func NewDecoder(expect map[string][]Column, want func(string) bool) *Decoder {
	return &Decoder{rels: make(map[uint32]*Relation), expect: expect, want: want}
}

// InTxn reports whether a transaction has begun but not yet committed.
func (d *Decoder) InTxn() bool { return d.cur != nil }

// Decode handles one raw pgoutput message. It returns a batch when the
// message was a Commit, and nil otherwise.
func (d *Decoder) Decode(walData []byte) (*TxnBatch, error) {
	msg, err := pglogrepl.Parse(walData)
	if err != nil {
		return nil, fmt.Errorf("cdc: parse pgoutput message: %w", err)
	}
	return d.Handle(msg)
}

// Handle is Decode for an already parsed message.
func (d *Decoder) Handle(msg pglogrepl.Message) (*TxnBatch, error) {
	switch m := msg.(type) {
	case *pglogrepl.RelationMessage:
		return nil, d.relation(m)
	case *pglogrepl.BeginMessage:
		if d.cur != nil {
			return nil, fmt.Errorf("%w: Begin inside transaction %d", ErrProtocol, d.cur.Xid)
		}
		d.cur = &TxnBatch{Xid: m.Xid, CommitTime: m.CommitTime, Deltas: make(map[string]*zset.ZSet)}
		return nil, nil
	case *pglogrepl.InsertMessage:
		rel, cur, err := d.rowTarget(m.RelationID, "Insert")
		if err != nil || rel == nil {
			return nil, err
		}
		row, err := tupleToRow(rel, m.Tuple, nil)
		if err != nil {
			return nil, err
		}
		cur.add(rel.Name, row, 1)
		return nil, nil
	case *pglogrepl.UpdateMessage:
		rel, cur, err := d.rowTarget(m.RelationID, "Update")
		if err != nil || rel == nil {
			return nil, err
		}
		if m.OldTuple == nil || m.OldTupleType != pglogrepl.UpdateMessageTupleTypeOld {
			return nil, fmt.Errorf("%w (update on %s)", ErrNoOldTuple, rel.Name)
		}
		old, err := tupleToRow(rel, m.OldTuple, nil)
		if err != nil {
			return nil, err
		}
		row, err := tupleToRow(rel, m.NewTuple, old)
		if err != nil {
			return nil, err
		}
		cur.add(rel.Name, old, -1)
		cur.add(rel.Name, row, 1)
		return nil, nil
	case *pglogrepl.DeleteMessage:
		rel, cur, err := d.rowTarget(m.RelationID, "Delete")
		if err != nil || rel == nil {
			return nil, err
		}
		if m.OldTuple == nil || m.OldTupleType != pglogrepl.DeleteMessageTupleTypeOld {
			return nil, fmt.Errorf("%w (delete on %s)", ErrNoOldTuple, rel.Name)
		}
		old, err := tupleToRow(rel, m.OldTuple, nil)
		if err != nil {
			return nil, err
		}
		cur.add(rel.Name, old, -1)
		return nil, nil
	case *pglogrepl.TruncateMessage:
		// Only a truncate of a table the views read is fatal; others in the
		// same publication do not affect any view.
		if len(m.RelationIDs) == 0 {
			return nil, ErrTruncate
		}
		for _, id := range m.RelationIDs {
			rel, ok := d.rels[id]
			if !ok || d.want == nil || d.want(rel.Name) {
				return nil, ErrTruncate
			}
		}
		return nil, nil
	case *pglogrepl.CommitMessage:
		if d.cur == nil {
			return nil, fmt.Errorf("%w: Commit without Begin", ErrProtocol)
		}
		b := d.cur
		d.cur = nil
		b.CommitLSN = uint64(m.CommitLSN)
		b.EndLSN = uint64(m.TransactionEndLSN)
		b.CommitTime = m.CommitTime
		return b, nil
	}
	// Type, Origin and logical decoding messages carry nothing we need.
	return nil, nil
}

func (d *Decoder) relation(m *pglogrepl.RelationMessage) error {
	rel := &Relation{ID: m.RelationID, Name: QualifiedName(m.Namespace, m.RelationName)}
	for _, c := range m.Columns {
		t, _ := types.TypeFromOID(c.DataType)
		rel.Columns = append(rel.Columns, Column{Name: c.Name, OID: c.DataType, Typmod: c.TypeModifier, Type: t})
	}
	if want, ok := d.expect[rel.Name]; ok && !SameColumns(want, rel.Columns) {
		return fmt.Errorf("%w: %s was %s, stream now says %s",
			ErrSchemaChanged, rel.Name, describe(want), describe(rel.Columns))
	}
	d.rels[m.RelationID] = rel
	return nil
}

func describe(cols []Column) string {
	s := "("
	for i, c := range cols {
		if i > 0 {
			s += ", "
		}
		s += fmt.Sprintf("%s oid %d typmod %d", c.Name, c.OID, c.Typmod)
	}
	return s + ")"
}

// rowTarget finds the relation for a row message. It returns a nil relation
// for tables the caller does not want.
func (d *Decoder) rowTarget(relID uint32, what string) (*Relation, *TxnBatch, error) {
	if d.cur == nil {
		return nil, nil, fmt.Errorf("%w: %s outside a transaction", ErrProtocol, what)
	}
	rel, ok := d.rels[relID]
	if !ok {
		return nil, nil, fmt.Errorf("%w: %s for unknown relation %d", ErrProtocol, what, relID)
	}
	d.cur.Changes++
	if d.want != nil && !d.want(rel.Name) {
		return nil, d.cur, nil
	}
	return rel, d.cur, nil
}

func (b *TxnBatch) add(table string, row types.Row, w int64) {
	z, ok := b.Deltas[table]
	if !ok {
		z = zset.New()
		b.Deltas[table] = z
	}
	z.Add(row, w)
}

// tupleToRow converts a pgoutput tuple to a Row. When old is not nil, an
// unchanged-TOAST marker copies the value from old: Postgres does not resend
// large values an UPDATE did not touch, and treating the marker as NULL
// would silently corrupt the row.
func tupleToRow(rel *Relation, t *pglogrepl.TupleData, old types.Row) (types.Row, error) {
	if t == nil {
		return nil, fmt.Errorf("%w: missing tuple for %s", ErrProtocol, rel.Name)
	}
	if len(t.Columns) != len(rel.Columns) {
		return nil, fmt.Errorf("%w: %s has %d columns, tuple has %d",
			ErrSchemaChanged, rel.Name, len(rel.Columns), len(t.Columns))
	}
	row := make(types.Row, len(t.Columns))
	for i, c := range t.Columns {
		col := rel.Columns[i]
		if col.Type == types.TUnknown {
			return nil, fmt.Errorf("%w: %s.%s has type oid %d", ErrUnknownType, rel.Name, col.Name, col.OID)
		}
		switch c.DataType {
		case pglogrepl.TupleDataTypeNull:
			row[i] = types.Null()
		case pglogrepl.TupleDataTypeToast:
			if old == nil {
				return nil, fmt.Errorf("%w: unchanged TOAST value for %s.%s with no old row",
					ErrNoOldTuple, rel.Name, col.Name)
			}
			row[i] = old[i]
		case pglogrepl.TupleDataTypeText:
			v, err := types.ParseText(col.Type, string(c.Data))
			if err != nil {
				return nil, fmt.Errorf("cdc: %s.%s: %w", rel.Name, col.Name, err)
			}
			row[i] = v
		default:
			return nil, fmt.Errorf("%w: tuple data type %q for %s.%s", ErrProtocol, c.DataType, rel.Name, col.Name)
		}
	}
	return row, nil
}
