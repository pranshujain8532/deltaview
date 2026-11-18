// Package ops holds the incremental operators (Filter, Project, Join,
// Aggregate, Distinct). Each turns an input delta into an output delta and
// keeps any state it needs in a state.Tx.
package ops
