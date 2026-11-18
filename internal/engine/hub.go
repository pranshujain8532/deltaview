package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/pranshujain8532/deltaview/internal/obs"
	"github.com/pranshujain8532/deltaview/internal/state"
	"github.com/pranshujain8532/deltaview/internal/zset"
)

// Event is one view delta, tagged with the LSN it brings the view to.
// Clients drop events with an LSN they already have: delivery to
// subscribers is at-least-once, not exactly-once.
type Event struct {
	LSN   uint64
	Delta *zset.ZSet
}

// Subscription is a client following one view: first a snapshot at LSN,
// then every delta after it on Events.
type Subscription struct {
	View   string
	LSN    uint64
	Rows   *zset.ZSet
	Events <-chan Event

	ch     chan Event
	resync atomic.Bool
	once   sync.Once
	hub    *hub
}

// Resync reports whether the subscription was dropped because the client
// fell behind. The client must start over from a fresh snapshot.
func (s *Subscription) Resync() bool { return s.resync.Load() }

// Close unregisters the subscription.
func (s *Subscription) Close() { s.hub.remove(s) }

func (s *Subscription) closeChan() { s.once.Do(func() { close(s.ch) }) }

// hub is the observer side of the engine: one writer (the apply loop)
// pushes deltas to many readers. A full buffer drops that reader instead of
// blocking the pipeline: one slow client must not stall every view.
type hub struct {
	mu      sync.Mutex
	subs    map[*Subscription]struct{}
	buf     int
	metrics *obs.Metrics
}

func newHub(buf int, m *obs.Metrics) *hub {
	return &hub{subs: make(map[*Subscription]struct{}), buf: buf, metrics: m}
}

func (h *hub) add(s *Subscription) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.subs[s] = struct{}{}
	if h.metrics != nil {
		h.metrics.Subscribers.Set(float64(len(h.subs)))
	}
}

func (h *hub) remove(s *Subscription) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.subs[s]; ok {
		delete(h.subs, s)
		s.closeChan()
		if h.metrics != nil {
			h.metrics.Subscribers.Set(float64(len(h.subs)))
		}
	}
}

func (h *hub) publish(view string, ev Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs {
		if s.View != view {
			continue
		}
		select {
		case s.ch <- ev:
		default:
			s.resync.Store(true)
			delete(h.subs, s)
			s.closeChan()
			if h.metrics != nil {
				h.metrics.SubscriberDrops.Inc()
				h.metrics.Subscribers.Set(float64(len(h.subs)))
			}
		}
	}
}

func (h *hub) closeAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs {
		delete(h.subs, s)
		s.closeChan()
	}
}

type subRequest struct {
	view  string
	reply chan subReply
}

type subReply struct {
	sub *Subscription
	err error
}

// Subscribe registers a subscriber. The request is served by the apply loop
// between two transactions: it pins a store snapshot and registers the
// subscriber in one step, so the snapshot's LSN and the first delta the
// subscriber receives are consecutive. Nothing is missed or repeated.
func (e *Engine) Subscribe(ctx context.Context, view string) (*Subscription, error) {
	if _, ok := e.byName[view]; !ok {
		return nil, fmt.Errorf("%w: %s", ErrNoView, view)
	}
	if !e.ready.Load() {
		return nil, ErrNotReady
	}
	req := subRequest{view: view, reply: make(chan subReply, 1)}
	select {
	case e.subReq <- req:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-e.stopped:
		return nil, errors.New("engine stopped")
	}
	r := <-req.reply
	return r.sub, r.err
}

// register runs on the apply loop.
func (e *Engine) register(req subRequest) {
	snap := e.store.Snapshot()
	defer snap.Close()
	lsn, err := state.GetLSN(snap, state.KeyAppliedLSN)
	if err != nil {
		req.reply <- subReply{err: err}
		return
	}
	rows, err := e.byName[req.view].ReadView(snap)
	if err != nil {
		req.reply <- subReply{err: err}
		return
	}
	ch := make(chan Event, e.cfg.SubBuffer)
	s := &Subscription{View: req.view, LSN: lsn, Rows: rows, Events: ch, ch: ch, hub: e.hub}
	e.hub.add(s)
	req.reply <- subReply{sub: s}
}
