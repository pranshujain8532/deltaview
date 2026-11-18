package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/pranshujain8532/deltaview/internal/cdc"
	"github.com/pranshujain8532/deltaview/internal/state"
	"github.com/pranshujain8532/deltaview/internal/zset"
)

// applyLoop is the single writer. For each batch of transactions:
//
//  1. skip a transaction whose EndLSN <= applied_lsn (a re-sent duplicate)
//  2. tx = store.Begin()
//  3. run every circuit with tx
//  4. tx.Set(m/applied_lsn, EndLSN)
//  5. tx.Commit(sync)  -- the durability point
//  6. acknowledge EndLSN to Postgres, only now
//  7. publish view deltas to subscribers, never blocking
//
// Between two batches it also registers subscribers, so a subscriber's
// snapshot and its first delta are adjacent.
func (e *Engine) applyLoop(ctx context.Context, readerErr <-chan error) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-readerErr:
			return err
		case req := <-e.subReq:
			e.register(req)
		case b := <-e.queue:
			batch := []*cdc.TxnBatch{b}
			for len(batch) < e.cfg.BatchMax {
				select {
				case more := <-e.queue:
					batch = append(batch, more)
					continue
				default:
				}
				break
			}
			if err := e.apply(ctx, batch); err != nil {
				return err
			}
		}
	}
}

func (e *Engine) apply(ctx context.Context, batch []*cdc.TxnBatch) error {
	m := e.cfg.Metrics
	if m != nil {
		m.QueueDepth.Set(float64(len(e.queue)))
	}
	prev := e.progress.Load()
	applied, hb := prev.LSN, prev.Heartbeat
	if e.cfg.Tracer != nil {
		var endSpan func()
		ctx, endSpan = e.startStep(ctx, len(batch), batch[len(batch)-1].EndLSN)
		defer endSpan()
	}
	t0 := time.Now()
	tx := e.store.Begin()
	outs := make(map[string]*zset.ZSet)
	var txns, rows int
	var commits []time.Time
	for _, b := range batch {
		if b.EndLSN <= applied {
			continue // re-sent after a restart; its effects are already stored
		}
		if e.cfg.PrintChanges {
			e.printBatch(b)
		}
		for _, c := range e.views {
			d, err := c.Step(ctx, tx, b.Deltas)
			if err != nil {
				tx.Discard()
				return fmt.Errorf("engine: transaction %d at %s: %w", b.Xid, cdc.FormatLSN(b.EndLSN), err)
			}
			if d.IsEmpty() {
				continue
			}
			if o, ok := outs[c.View]; ok {
				o.Merge(d)
			} else {
				outs[c.View] = d
			}
		}
		if z := b.Deltas[e.cfg.Heartbeat]; z != nil {
			for _, en := range z.Entries() {
				if v := en.Row[e.hbSeqCol]; en.Weight > 0 && !v.IsNull() && v.AsInt() > hb {
					hb = v.AsInt()
				}
			}
		}
		applied = b.EndLSN
		txns++
		rows += b.Changes
		commits = append(commits, b.CommitTime)
	}
	if txns == 0 {
		tx.Discard()
		return nil
	}
	tx.Set(state.KeyAppliedLSN, state.EncodeInt(int64(applied)))
	tx.Set(state.KeyHeartbeat, state.EncodeInt(hb))
	applyDur := time.Since(t0)

	e.cfg.Failpoints.Hit(FailBeforeCommit)
	t1 := time.Now()
	if err := tx.Commit(!e.cfg.NoSync); err != nil {
		return fmt.Errorf("engine: commit at %s: %w", cdc.FormatLSN(applied), err)
	}
	commitDur := time.Since(t1)
	e.cfg.Failpoints.Hit(FailAfterCommit)

	// The batch is durable. Only now may Postgres forget this WAL.
	e.progress.Store(&Progress{LSN: applied, Heartbeat: hb})
	e.acked.Store(applied)

	now := time.Now()
	for view, d := range outs {
		if !d.IsEmpty() {
			e.hub.publish(view, Event{LSN: applied, Delta: d})
		}
	}
	if m != nil {
		m.Txns.Add(float64(txns))
		m.Rows.Add(float64(rows))
		m.Commits.Inc()
		m.AppliedLSN.Set(float64(applied))
		m.ApplySeconds.Observe(applyDur.Seconds())
		m.CommitSeconds.Observe(commitDur.Seconds())
		for _, ct := range commits {
			if !ct.IsZero() {
				m.EndToEnd.Observe(now.Sub(ct).Seconds())
			}
		}
		for view, d := range outs {
			m.OutputRows.WithLabelValues(view).Add(float64(d.Len()))
			m.DeltaRows.Observe(float64(d.Len()))
		}
	}
	return nil
}

func (e *Engine) startStep(ctx context.Context, txns int, end uint64) (context.Context, func()) {
	ctx, span := e.cfg.Tracer.StartStep(ctx, txns, end)
	return ctx, func() { span.End() }
}

func (e *Engine) printBatch(b *cdc.TxnBatch) {
	var sb strings.Builder
	fmt.Fprintf(&sb, "txn %d end=%s changes=%d\n", b.Xid, cdc.FormatLSN(b.EndLSN), b.Changes)
	for table, z := range b.Deltas {
		for _, en := range z.Entries() {
			fmt.Fprintf(&sb, "  %s %+d %v\n", table, en.Weight, en.Row)
		}
	}
	fmt.Print(sb.String())
}
