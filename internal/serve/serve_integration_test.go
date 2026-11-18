//go:build integration

package serve

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pranshujain8532/deltaview/internal/cdc"
	"github.com/pranshujain8532/deltaview/internal/engine"
	"github.com/pranshujain8532/deltaview/internal/state"
)

func TestHTTPViewAndSubscribe(t *testing.T) {
	url := os.Getenv("DV_PG_URL")
	if url == "" {
		t.Skip("DV_PG_URL not set")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	const slot = "serve_it_slot"
	if err := cdc.DropSlot(ctx, db, slot); err != nil {
		t.Fatal(err)
	}
	defer cdc.DropSlot(context.Background(), db, slot)
	for _, s := range []string{"DELETE FROM orders", "INSERT INTO orders VALUES (1, 1, 'paid', 10.50, now())"} {
		if _, err := db.Exec(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	e, err := engine.New(ctx, engine.Config{PGURL: url, Slot: slot, Store: state.NewMemory(),
		ViewsSQL: "CREATE MATERIALIZED VIEW per_status AS SELECT status, COUNT(*) AS n, SUM(amount) AS total FROM orders GROUP BY status;"})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	for !e.Ready() {
		time.Sleep(10 * time.Millisecond)
	}
	srv := httptest.NewServer(New(e, nil, Options{SQLConsole: true, Token: "t0k", PGURL: url}))
	defer srv.Close()

	var view struct {
		LSN     string  `json:"lsn"`
		Columns []any   `json:"columns"`
		Rows    [][]any `json:"rows"`
	}
	resp, err := http.Get(srv.URL + "/views/per_status")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewDecoder(resp.Body).Decode(&view); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("read-only API must allow any origin, got %q", got)
	}
	if len(view.Rows) != 1 || view.Rows[0][0] != "paid" || view.Rows[0][2] != 10.5 || view.LSN == "" {
		t.Fatalf("GET view: %+v", view)
	}
	if r, _ := http.Get(srv.URL + "/views/nope"); r.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown view: %d", r.StatusCode)
	}

	resp, err = http.Get(srv.URL + "/views/per_status/subscribe")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	events := make(chan [2]string, 10)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		var name string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				events <- [2]string{name, strings.TrimPrefix(line, "data: ")}
			}
		}
	}()
	next := func() [2]string {
		select {
		case ev := <-events:
			return ev
		case <-time.After(10 * time.Second):
			t.Fatal("no SSE event")
		}
		return [2]string{}
	}
	if ev := next(); ev[0] != "snapshot" || !strings.Contains(ev[1], `"row":["paid",1,10.5]`) {
		t.Fatalf("first event %v", ev)
	}
	if _, err := db.Exec(ctx, "INSERT INTO orders VALUES (2, 1, 'paid', 1, now())"); err != nil {
		t.Fatal(err)
	}
	ev := next()
	if ev[0] != "delta" || !strings.Contains(ev[1], `{"row":["paid",1,10.5],"weight":-1}`) ||
		!strings.Contains(ev[1], `{"row":["paid",2,11.5],"weight":1}`) {
		t.Fatalf("delta event %v", ev)
	}

	// The SQL console: a wrong token is refused; the right one runs the
	// statement, and the change reaches subscribers through the WAL.
	post := func(token, sql string) (*http.Response, map[string]any) {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/sql", strings.NewReader(`{"sql":`+strconv.Quote(sql)+`}`))
		req.Header.Set("X-DeltaView-Token", token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var body map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&body)
		return resp, body
	}
	if resp, _ := post("wrong", "DELETE FROM orders"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token: status %d", resp.StatusCode)
	}
	resp, body := post("t0k", "UPDATE orders SET amount = 5 WHERE id = 2")
	if resp.StatusCode != http.StatusOK || body["error"] != nil {
		t.Fatalf("console update: %d %v", resp.StatusCode, body)
	}
	if tag := body["results"].([]any)[0].(map[string]any)["tag"]; tag != "UPDATE 1" {
		t.Fatalf("command tag %v", tag)
	}
	ev = next()
	if ev[0] != "delta" || !strings.Contains(ev[1], `{"row":["paid",2,15.5],"weight":1}`) {
		t.Fatalf("console change did not reach the subscriber: %v", ev)
	}
	page, err := http.Get(srv.URL + "/console")
	if err != nil {
		t.Fatal(err)
	}
	html, _ := io.ReadAll(page.Body)
	page.Body.Close()
	if page.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("pages must not be readable cross-origin: they carry the console token")
	}
	if page.StatusCode == http.StatusOK && !strings.Contains(string(html), `<meta name="dv-token" content="t0k">`) {
		t.Fatal("console page is missing the injected token")
	}
	cancel()
	<-done
}
