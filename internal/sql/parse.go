package sql

import (
	"errors"
	"fmt"
	"strings"

	pg "github.com/pganalyze/pg_query_go/v6"
)

// ErrUnsupported is wrapped by every rejection of SQL that DeltaView cannot
// maintain incrementally. The message always names the feature.
var ErrUnsupported = errors.New("unsupported SQL")

func unsupported(feature, why string) error {
	if why == "" {
		return fmt.Errorf("%w: %s", ErrUnsupported, feature)
	}
	return fmt.Errorf("%w: %s (%s)", ErrUnsupported, feature, why)
}

// ViewDef is one view from views.sql.
type ViewDef struct {
	Name   string
	Query  string // the SELECT, deparsed by the Postgres parser
	Select *pg.SelectStmt
}

// ParseViews reads a file of CREATE MATERIALIZED VIEW name AS SELECT ...
// (or CREATE VIEW) statements using the real Postgres parser.
func ParseViews(src string) ([]ViewDef, error) {
	tree, err := pg.Parse(src)
	if err != nil {
		return nil, fmt.Errorf("parse views: %w", err)
	}
	var defs []ViewDef
	seen := make(map[string]bool)
	for i, raw := range tree.GetStmts() {
		var name string
		var query *pg.Node
		switch {
		case raw.GetStmt().GetCreateTableAsStmt() != nil:
			c := raw.GetStmt().GetCreateTableAsStmt()
			if c.GetObjtype() != pg.ObjectType_OBJECT_MATVIEW {
				return nil, fmt.Errorf("statement %d: only CREATE MATERIALIZED VIEW and CREATE VIEW are allowed", i+1)
			}
			if c.GetInto().GetSkipData() {
				return nil, fmt.Errorf("statement %d: %w", i+1, unsupported("WITH NO DATA", ""))
			}
			name = c.GetInto().GetRel().GetRelname()
			query = c.GetQuery()
		case raw.GetStmt().GetViewStmt() != nil:
			v := raw.GetStmt().GetViewStmt()
			name = v.GetView().GetRelname()
			query = v.GetQuery()
			if len(v.GetAliases()) > 0 {
				return nil, fmt.Errorf("view %s: %w", name, unsupported("column alias list on the view", "alias the select list instead"))
			}
		default:
			return nil, fmt.Errorf("statement %d: only CREATE MATERIALIZED VIEW and CREATE VIEW are allowed", i+1)
		}
		sel := query.GetSelectStmt()
		if sel == nil {
			return nil, fmt.Errorf("view %s: the query must be a SELECT", name)
		}
		if seen[name] {
			return nil, fmt.Errorf("view %s is defined twice", name)
		}
		seen[name] = true
		text, err := pg.Deparse(&pg.ParseResult{Version: tree.GetVersion(), Stmts: []*pg.RawStmt{{Stmt: query}}})
		if err != nil {
			return nil, fmt.Errorf("view %s: deparse: %w", name, err)
		}
		defs = append(defs, ViewDef{Name: name, Query: strings.TrimSpace(text), Select: sel})
	}
	return defs, nil
}

// ParseSelect parses a single SELECT, for tests and ad-hoc planning.
func ParseSelect(name, query string) (ViewDef, error) {
	defs, err := ParseViews(fmt.Sprintf("CREATE MATERIALIZED VIEW %s AS %s", name, query))
	if err != nil {
		return ViewDef{}, err
	}
	return defs[0], nil
}
