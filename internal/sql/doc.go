// Package sql turns view definitions written in SQL into logical plans:
// parse with the real Postgres parser, bind names against table schemas, and
// reject every feature that has no proven incremental rule.
package sql
