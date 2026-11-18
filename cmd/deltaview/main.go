// Command deltaview runs the DeltaView engine. In this milestone it only
// reads the replication stream; --print-changes prints each transaction.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"

	"github.com/jackc/pgx/v5"

	"github.com/pranshujain8532/deltaview/internal/cdc"
)

func main() {
	pgURL := flag.String("pg", envOr("DV_PG_URL", "postgres://deltaview:deltaview@localhost:5432/deltaview?sslmode=disable"), "Postgres connection URL")
	slot := flag.String("slot", "deltaview_slot", "replication slot name")
	pub := flag.String("publication", "deltaview_pub", "publication name")
	print := flag.Bool("print-changes", false, "print every transaction as it arrives")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, *pgURL, *slot, *pub, *print); err != nil && ctx.Err() == nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, pgURL, slot, pub string, print bool) error {
	db, err := pgx.Connect(ctx, pgURL)
	if err != nil {
		return err
	}
	defer db.Close(context.Background())
	info, err := cdc.GetSlot(ctx, db, slot)
	if err != nil {
		return err
	}
	repl, err := cdc.ConnectReplication(ctx, pgURL)
	if err != nil {
		return err
	}
	defer repl.Close(context.Background())
	start := info.ConfirmedFlush
	if !info.Exists {
		if start, _, err = cdc.CreateSlot(ctx, repl, slot); err != nil {
			return err
		}
	}
	var acked atomic.Uint64
	acked.Store(start)
	out := make(chan *cdc.TxnBatch, 64)
	r := cdc.NewReader(repl, cdc.ReaderConfig{Slot: slot, Publication: pub}, cdc.NewDecoder(nil, nil), &acked)
	errc := make(chan error, 1)
	go func() { errc <- r.Run(ctx, start, out) }()
	log.Printf("streaming slot %s from %s", slot, cdc.FormatLSN(start))
	for {
		select {
		case b := <-out:
			if print {
				fmt.Printf("txn %d end=%s commit=%s changes=%d\n", b.Xid, cdc.FormatLSN(b.EndLSN),
					b.CommitTime.UTC().Format("15:04:05.000"), b.Changes)
				for table, z := range b.Deltas {
					for _, e := range z.Entries() {
						fmt.Printf("  %s %+d %v\n", table, e.Weight, e.Row)
					}
				}
			}
			// M1 only: acknowledge after handing the batch over. M8 moves this
			// behind a durable commit.
			acked.Store(b.EndLSN)
		case err := <-errc:
			return err
		}
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
