package serve

import (
	"bytes"
	"embed"
	"html"
	"io/fs"
	"net/http"
)

// web holds the built website (site/ in the repository, copied here by
// `make site`). Serving it from the engine means `docker compose up` gives
// a page that is already connected to a real engine, on the same origin.
//
//go:embed all:web
var web embed.FS

func webFS() fs.FS {
	sub, err := fs.Sub(web, "web")
	if err != nil {
		return web // unreachable: "web" is embedded above
	}
	return sub
}

func staticFiles() http.Handler { return http.FileServer(http.FS(webFS())) }

// page serves an HTML page with two meta tags injected: one telling the
// site that the engine is this same origin, and, when the SQL console is
// on, the token it must send. No CORS header is set on pages, so other
// origins cannot read the token.
func (s *Server) page(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		b, err := fs.ReadFile(webFS(), name)
		if err != nil {
			http.Error(w, "page not built: run make site", http.StatusNotFound)
			return
		}
		meta := `<meta name="dv-engine" content="same-origin">`
		if s.opts.SQLConsole {
			meta += `<meta name="dv-token" content="` + html.EscapeString(s.opts.Token) + `">`
		}
		b = bytes.Replace(b, []byte("</head>"), []byte(meta+"</head>"), 1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(b)
	}
}
