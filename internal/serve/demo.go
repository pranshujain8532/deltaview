package serve

import (
	_ "embed"
	"net/http"
)

//go:embed demo.html
var demoPage []byte

// demo serves a small page that subscribes to a view over SSE and shows it
// changing live (default view: revenue_by_city, or ?view=NAME).
func (s *Server) demo(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(demoPage)
}
