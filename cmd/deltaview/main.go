// Command deltaview runs the DeltaView engine: it reads Postgres logical
// replication, keeps every view in views.sql up to date incrementally and
// serves them over HTTP.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/pranshujain8532/deltaview/internal/engine"
	"github.com/pranshujain8532/deltaview/internal/obs"
	"github.com/pranshujain8532/deltaview/internal/serve"
	"github.com/pranshujain8532/deltaview/internal/state"
)

func main() {
	var (
		pgURL     = flag.String("pg", envOr("DV_PG_URL", "postgres://deltaview:deltaview@localhost:5432/deltaview?sslmode=disable"), "Postgres connection URL")
		views     = flag.String("views", envOr("DV_VIEWS", "views.sql"), "file with CREATE MATERIALIZED VIEW statements")
		stateDir  = flag.String("state-dir", envOr("DV_STATE_DIR", "data"), "Pebble state directory")
		memory    = flag.Bool("memory", false, "keep state in memory (re-snapshot on every start)")
		addr      = flag.String("http", envOr("DV_HTTP", ":8080"), "HTTP listen address")
		slot      = flag.String("slot", "deltaview_slot", "replication slot name")
		pub       = flag.String("publication", "deltaview_pub", "publication name")
		batchMax  = flag.Int("batch-max", 1, "transactions per state commit (group commit)")
		noSync    = flag.Bool("no-sync", false, "commit without fsync (benchmarks only; breaks crash safety)")
		reset     = flag.Bool("reset", false, "discard state and take a fresh snapshot")
		printChgs = flag.Bool("print-changes", false, "print every transaction's row changes")
		sqlCons   = flag.Bool("sql-console", os.Getenv("DV_SQL_CONSOLE") == "1", "let the website run SQL on Postgres (token protected)")
		token     = flag.String("console-token", os.Getenv("DV_CONSOLE_TOKEN"), "token for the SQL console (random if empty)")
		verbose   = flag.Bool("v", false, "debug logging")
	)
	flag.Parse()
	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, log, runConfig{*pgURL, *views, *stateDir, *memory, *addr, *slot, *pub, *batchMax,
		*noSync, *reset, *printChgs, *sqlCons, *token}); err != nil {
		log.Error("deltaview stopped", "err", err)
		os.Exit(1)
	}
}

type runConfig struct {
	pgURL, views, stateDir string
	memory                 bool
	addr, slot, pub        string
	batchMax               int
	noSync, reset, print   bool
	sqlConsole             bool
	token                  string
}

func run(ctx context.Context, log *slog.Logger, rc runConfig) error {
	src, err := os.ReadFile(rc.views)
	if err != nil {
		return fmt.Errorf("read views: %w", err)
	}
	var store state.Store
	if rc.memory {
		store = state.NewMemory()
	} else {
		p, err := state.OpenPebble(rc.stateDir)
		if err != nil {
			return err
		}
		store = p
	}
	defer store.Close()

	fp, err := engine.FailpointsFromEnv()
	if err != nil {
		return err
	}
	shutdownTracing, err := obs.SetupTracing(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = shutdownTracing(context.Background()) }()
	metrics := obs.NewMetrics(func() float64 { return float64(store.SizeBytes()) })

	e, err := engine.New(ctx, engine.Config{
		PGURL: rc.pgURL, Slot: rc.slot, Publication: rc.pub, ViewsSQL: string(src), Store: store,
		BatchMax: rc.batchMax, NoSync: rc.noSync, Reset: rc.reset, PrintChanges: rc.print,
		Logger: log, Metrics: metrics, Tracer: obs.NewTracer(), Failpoints: fp,
	})
	if err != nil {
		return err
	}

	ln, err := net.Listen("tcp", rc.addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", rc.addr, err)
	}
	if rc.sqlConsole && rc.token == "" {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return err
		}
		rc.token = hex.EncodeToString(b)
	}
	handler := serve.New(e, metrics, serve.Options{SQLConsole: rc.sqlConsole, Token: rc.token, PGURL: rc.pgURL})
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server", "err", err)
		}
	}()
	log.Info("serving", "addr", ln.Addr().String(), "website", "http://localhost"+portOf(ln.Addr().String()))
	if rc.sqlConsole {
		log.Info("SQL console on; open the console from this engine, or connect the public site with this link",
			"link", "https://pranshujain8532.github.io/deltaview/console.html#engine=http://localhost"+portOf(ln.Addr().String())+"&token="+rc.token)
	}
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	return e.Run(ctx)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// portOf returns ":PORT" from a listen address.
func portOf(addr string) string {
	if i := strings.LastIndexByte(addr, ':'); i >= 0 {
		return addr[i:]
	}
	return addr
}
