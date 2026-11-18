// Package zset implements Z-sets: maps from a row to an integer weight.
// A delta (insert = +1, delete = -1, update = -old +new) and a table are both
// Z-sets, so every operator needs only one rule: input delta in, output delta out.
package zset
