package serve

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

// maxRows caps how many rows of a SELECT the console returns.
const maxRows = 200

type sqlResult struct {
	Tag     string      `json:"tag"`
	Columns []string    `json:"columns,omitempty"`
	Rows    [][]*string `json:"rows,omitempty"`
	More    bool        `json:"more,omitempty"`
}

// sql runs statements on Postgres for the website's SQL console, so a user
// can change data and watch the views follow. The engine itself only ever
// sees the result through the replication stream, like any other write.
func (s *Server) sql(w http.ResponseWriter, r *http.Request) {
	if !s.opts.SQLConsole {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "the SQL console is off; start deltaview with --sql-console (or run the statement in psql)"})
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-DeltaView-Token")), []byte(s.opts.Token)) != 1 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing or wrong console token"})
		return
	}
	var req struct {
		SQL string `json:"sql"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil || req.SQL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must be {\"sql\": \"...\"}"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, s.opts.PGURL)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "connect to postgres: " + err.Error()})
		return
	}
	defer conn.Close(context.Background())
	if _, err := conn.Exec(ctx, "SET statement_timeout = '5s'"); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	t0 := time.Now()
	results, err := conn.PgConn().Exec(ctx, req.SQL).ReadAll()
	out := make([]sqlResult, 0, len(results))
	for _, res := range results {
		sr := sqlResult{Tag: res.CommandTag.String()}
		for _, fd := range res.FieldDescriptions {
			sr.Columns = append(sr.Columns, fd.Name)
		}
		for i, row := range res.Rows {
			if i == maxRows {
				sr.More = true
				break
			}
			cells := make([]*string, len(row))
			for j, b := range row {
				if b != nil {
					v := string(b)
					cells[j] = &v
				}
			}
			sr.Rows = append(sr.Rows, cells)
		}
		out = append(out, sr)
	}
	body := map[string]any{"results": out, "ms": time.Since(t0).Milliseconds()}
	if err != nil {
		var msg = err.Error()
		if errors.Is(err, context.DeadlineExceeded) {
			msg = "timed out"
		}
		body["error"] = msg
		writeJSON(w, http.StatusOK, body)
		return
	}
	writeJSON(w, http.StatusOK, body)
}
