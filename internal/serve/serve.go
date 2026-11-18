package serve

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/pranshujain8532/deltaview/internal/cdc"
	"github.com/pranshujain8532/deltaview/internal/circuit"
	"github.com/pranshujain8532/deltaview/internal/engine"
	"github.com/pranshujain8532/deltaview/internal/obs"
	"github.com/pranshujain8532/deltaview/internal/types"
	"github.com/pranshujain8532/deltaview/internal/zset"
)

// Server is the HTTP front of the engine. The view handlers only read:
// views from store snapshots, deltas from subscriptions. They never touch
// state. The optional SQL console writes to Postgres, never to the engine.
type Server struct {
	e         *engine.Engine
	metrics   *obs.Metrics
	mux       *http.ServeMux
	opts      Options
	Keepalive time.Duration
}

// Options configures the optional parts of the server.
type Options struct {
	// SQLConsole enables POST /sql, which runs statements on Postgres so a
	// user can make a change and watch it flow through the views.
	SQLConsole bool
	// Token must be sent as X-DeltaView-Token with every /sql request. It is
	// injected only into pages this server serves itself, which other
	// origins cannot read, so no other website can run SQL here.
	Token string
	// PGURL is the database /sql connects to.
	PGURL string
}

// New builds the handler for every endpoint.
func New(e *engine.Engine, m *obs.Metrics, opts Options) *Server {
	s := &Server{e: e, metrics: m, mux: http.NewServeMux(), opts: opts, Keepalive: 15 * time.Second}
	api := func(pattern string, h http.HandlerFunc) { s.mux.HandleFunc(pattern, cors(h)) }
	api("GET /views", s.listViews)
	api("GET /views/{name}", s.getView)
	api("GET /views/{name}/subscribe", s.subscribe)
	api("GET /status", s.status)
	api("POST /sql", s.sql)
	api("OPTIONS /sql", s.preflight)
	if m != nil {
		h := promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{})
		api("GET /metrics", h.ServeHTTP)
	}
	s.mux.HandleFunc("GET /{$}", s.page("index.html"))
	s.mux.HandleFunc("GET /console", s.page("console.html"))
	s.mux.HandleFunc("GET /demo", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/console", http.StatusFound)
	})
	s.mux.Handle("GET /", staticFiles())
	return s
}

// cors lets any origin read the API: the showcase site on GitHub Pages can
// then follow a locally running engine. Pages (HTML) do not get this header.
func cors(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		h(w, r)
	}
}

func (s *Server) preflight(w http.ResponseWriter, _ *http.Request) {
	h := w.Header()
	h.Set("Access-Control-Allow-Methods", "POST")
	h.Set("Access-Control-Allow-Headers", "Content-Type, X-DeltaView-Token")
	h.Set("Access-Control-Allow-Private-Network", "true")
	h.Set("Access-Control-Max-Age", "600")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

type columnJSON struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

func columns(cs []circuit.Column) []columnJSON {
	out := make([]columnJSON, len(cs))
	for i, c := range cs {
		out[i] = columnJSON{Name: c.Name, Type: c.Type.String()}
	}
	return out
}

func (s *Server) listViews(w http.ResponseWriter, _ *http.Request) {
	type view struct {
		Name    string       `json:"name"`
		SQL     string       `json:"sql"`
		Columns []columnJSON `json:"columns"`
	}
	var out []view
	for _, v := range s.e.Views() {
		out = append(out, view{Name: v.Name, SQL: v.SQL, Columns: columns(v.Columns)})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	switch {
	case errors.Is(err, engine.ErrNoView):
		code = http.StatusNotFound
	case errors.Is(err, engine.ErrNotReady):
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

// getView returns the view's rows plus the LSN they are valid at. A row with
// weight 2 appears twice: a view is a bag, like a SQL result.
func (s *Server) getView(w http.ResponseWriter, r *http.Request) {
	info, rows, lsn, err := s.e.ReadView(r.PathValue("name"))
	if err != nil {
		s.fail(w, err)
		return
	}
	b := []byte(`{"view":`)
	b = strconv.AppendQuote(b, info.Name)
	b = appendLSN(b, lsn)
	b = append(b, `,"columns":`...)
	cj, _ := json.Marshal(columns(info.Columns))
	b = append(b, cj...)
	b = append(b, `,"rows":[`...)
	first := true
	for _, e := range rows.Entries() {
		for i := int64(0); i < e.Weight; i++ {
			if !first {
				b = append(b, ',')
			}
			first = false
			b = appendRow(b, e.Row)
		}
	}
	b = append(b, "]}\n"...)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(b)
}

func appendLSN(b []byte, lsn uint64) []byte {
	b = append(b, `,"lsn":`...)
	b = strconv.AppendQuote(b, cdc.FormatLSN(lsn))
	b = append(b, `,"lsn_int":`...)
	return strconv.AppendUint(b, lsn, 10)
}

func appendRow(b []byte, r types.Row) []byte {
	b = append(b, '[')
	for i, v := range r {
		if i > 0 {
			b = append(b, ',')
		}
		b = types.AppendJSON(b, v)
	}
	return append(b, ']')
}

func appendChanges(b []byte, d *zset.ZSet) []byte {
	b = append(b, `"changes":[`...)
	for i, e := range d.Entries() {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, `{"row":`...)
		b = appendRow(b, e.Row)
		b = append(b, `,"weight":`...)
		b = strconv.AppendInt(b, e.Weight, 10)
		b = append(b, '}')
	}
	return append(b, ']')
}

// subscribe streams server-sent events: one "snapshot" event with the rows
// and their LSN, then a "delta" event per transaction that changed the
// view. A client that falls behind gets a "resync" event and is closed.
func (s *Server) subscribe(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	sub, err := s.e.Subscribe(r.Context(), r.PathValue("name"))
	if err != nil {
		s.fail(w, err)
		return
	}
	defer sub.Close()
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	info := s.e.Views()
	var cols []circuit.Column
	for _, v := range info {
		if v.Name == sub.View {
			cols = v.Columns
		}
	}
	b := []byte(`{"view":`)
	b = strconv.AppendQuote(b, sub.View)
	b = appendLSN(b, sub.LSN)
	cj, _ := json.Marshal(columns(cols))
	b = append(b, `,"columns":`...)
	b = append(b, cj...)
	b = append(b, ',')
	b = appendChanges(b, sub.Rows)
	b = append(b, '}')
	if err := writeEvent(w, "snapshot", sub.LSN, b); err != nil {
		return
	}
	flusher.Flush()

	tick := time.NewTicker(s.Keepalive)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case ev, ok := <-sub.Events:
			if !ok {
				if sub.Resync() {
					_ = writeEvent(w, "resync", sub.LSN, []byte(`{"reason":"subscriber fell behind; fetch a new snapshot"}`))
					flusher.Flush()
				}
				return
			}
			b := []byte(`{"view":`)
			b = strconv.AppendQuote(b, sub.View)
			b = appendLSN(b, ev.LSN)
			b = append(b, ',')
			b = appendChanges(b, ev.Delta)
			b = append(b, '}')
			if err := writeEvent(w, "delta", ev.LSN, b); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func writeEvent(w http.ResponseWriter, name string, lsn uint64, data []byte) error {
	_, err := fmt.Fprintf(w, "event: %s\nid: %d\ndata: %s\n\n", name, lsn, data)
	return err
}

func (s *Server) status(w http.ResponseWriter, _ *http.Request) {
	p := s.e.Progress()
	views := make([]string, 0)
	for _, v := range s.e.Views() {
		views = append(views, v.Name)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ready":           s.e.Ready(),
		"applied_lsn":     cdc.FormatLSN(p.LSN),
		"applied_lsn_int": p.LSN,
		"heartbeat":       p.Heartbeat,
		"slot_lag_bytes":  s.e.SlotLag(),
		"queue_depth":     s.e.QueueDepth(),
		"sql_console":     s.opts.SQLConsole,
		"views":           views,
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
