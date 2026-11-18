// Package types defines the SQL values DeltaView understands (Value, Row) and
// their canonical, order-preserving byte encoding. Rows are used as map keys
// and as storage keys, so two equal rows must always encode to the same bytes.
package types
