package cdc

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/jackc/pglogrepl"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
)

// ConnectReplication opens a connection in logical replication mode
// (replication=database). Output functions run with TimeZone UTC and ISO
// dates so timestamps arrive in one known text format.
func ConnectReplication(ctx context.Context, connString string) (*pgconn.PgConn, error) {
	cfg, err := pgconn.ParseConfig(connString)
	if err != nil {
		return nil, fmt.Errorf("cdc: parse connection string: %w", err)
	}
	cfg.RuntimeParams["replication"] = "database"
	cfg.RuntimeParams["timezone"] = "UTC"
	cfg.RuntimeParams["datestyle"] = "ISO, MDY"
	conn, err := pgconn.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("cdc: connect for replication: %w", err)
	}
	return conn, nil
}

// ReaderConfig configures one replication stream.
type ReaderConfig struct {
	Slot        string
	Publication string
	// StatusInterval is the longest gap between status updates. Postgres drops
	// a silent client after wal_sender_timeout (60 s by default).
	StatusInterval time.Duration
	// AckPoll is how often the reader checks for a newly committed LSN to ack.
	AckPoll time.Duration
}

func (c *ReaderConfig) defaults() {
	if c.StatusInterval <= 0 {
		c.StatusInterval = 5 * time.Second
	}
	if c.AckPoll <= 0 {
		c.AckPoll = 50 * time.Millisecond
	}
}

// Reader streams one replication slot into TxnBatches. It is the only
// goroutine that touches the replication connection: it receives messages,
// answers keepalives and sends acknowledgements.
type Reader struct {
	conn    *pgconn.PgConn
	cfg     ReaderConfig
	dec     *Decoder
	acked   *atomic.Uint64
	sentAck uint64
	lastMsg time.Time
	// OnAck, when set, is called after a status update that acknowledged a
	// new LSN was sent. The engine uses it for the after-ack fail point.
	OnAck func(lsn uint64)
	// OnKeepalive, when set, receives the server's WAL end from keepalives.
	OnKeepalive func(serverWALEnd uint64)
}

// NewReader builds a reader. acked is the LSN the engine has durably
// committed; it is the only position ever reported to Postgres as flushed.
func NewReader(conn *pgconn.PgConn, cfg ReaderConfig, dec *Decoder, acked *atomic.Uint64) *Reader {
	cfg.defaults()
	return &Reader{conn: conn, cfg: cfg, dec: dec, acked: acked}
}

// Run starts replication at start and sends each committed transaction to
// out, blocking when out is full (backpressure: Postgres keeps the WAL).
// It returns when ctx is cancelled or on the first error.
func (r *Reader) Run(ctx context.Context, start uint64, out chan<- *TxnBatch) error {
	err := pglogrepl.StartReplication(ctx, r.conn, r.cfg.Slot, pglogrepl.LSN(start),
		pglogrepl.StartReplicationOptions{
			Mode: pglogrepl.LogicalReplication,
			PluginArgs: []string{
				"proto_version '1'",
				fmt.Sprintf("publication_names '%s'", r.cfg.Publication),
			},
		})
	if err != nil {
		return fmt.Errorf("cdc: start replication on slot %s at %s: %w", r.cfg.Slot, pglogrepl.LSN(start), err)
	}
	r.sentAck = start
	r.lastMsg = time.Now()
	if err := r.sendStatus(ctx, false); err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := r.maybeAck(ctx); err != nil {
			return err
		}
		batch, err := r.receive(ctx)
		if err != nil {
			return err
		}
		if batch == nil {
			continue
		}
		if err := r.deliver(ctx, batch, out); err != nil {
			return err
		}
	}
}

// deliver hands a batch to the apply loop. While the queue is full it keeps
// acknowledging, so a slow apply step does not look like a dead client.
func (r *Reader) deliver(ctx context.Context, b *TxnBatch, out chan<- *TxnBatch) error {
	tick := time.NewTicker(r.cfg.AckPoll)
	defer tick.Stop()
	for {
		select {
		case out <- b:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
			if err := r.maybeAck(ctx); err != nil {
				return err
			}
		}
	}
}

// receive waits up to AckPoll for one message and returns a batch when that
// message completed a transaction.
func (r *Reader) receive(ctx context.Context) (*TxnBatch, error) {
	rctx, cancel := context.WithTimeout(ctx, r.cfg.AckPoll)
	raw, err := r.conn.ReceiveMessage(rctx)
	cancel()
	if err != nil {
		if pgconn.Timeout(err) && ctx.Err() == nil {
			return nil, nil
		}
		return nil, fmt.Errorf("cdc: receive: %w", err)
	}
	switch msg := raw.(type) {
	case *pgproto3.CopyData:
		if len(msg.Data) == 0 {
			return nil, nil
		}
		switch msg.Data[0] {
		case pglogrepl.PrimaryKeepaliveMessageByteID:
			ka, err := pglogrepl.ParsePrimaryKeepaliveMessage(msg.Data[1:])
			if err != nil {
				return nil, fmt.Errorf("cdc: keepalive: %w", err)
			}
			if r.OnKeepalive != nil {
				r.OnKeepalive(uint64(ka.ServerWALEnd))
			}
			if ka.ReplyRequested {
				return nil, r.sendStatus(ctx, false)
			}
		case pglogrepl.XLogDataByteID:
			xld, err := pglogrepl.ParseXLogData(msg.Data[1:])
			if err != nil {
				return nil, fmt.Errorf("cdc: xlogdata: %w", err)
			}
			if r.OnKeepalive != nil {
				r.OnKeepalive(uint64(xld.ServerWALEnd))
			}
			return r.dec.Decode(xld.WALData)
		}
	case *pgproto3.ErrorResponse:
		return nil, fmt.Errorf("cdc: server error: %s (%s)", msg.Message, msg.Code)
	case *pgproto3.CopyDone:
		return nil, errors.New("cdc: server ended the replication stream")
	}
	return nil, nil
}

// maybeAck sends a status update when the committed LSN moved, or when the
// status interval has passed.
func (r *Reader) maybeAck(ctx context.Context) error {
	if r.acked.Load() > r.sentAck || time.Since(r.lastMsg) >= r.cfg.StatusInterval {
		return r.sendStatus(ctx, false)
	}
	return nil
}

func (r *Reader) sendStatus(ctx context.Context, reply bool) error {
	lsn := r.acked.Load()
	if lsn < r.sentAck {
		lsn = r.sentAck // never move backwards
	}
	err := pglogrepl.SendStandbyStatusUpdate(ctx, r.conn, pglogrepl.StandbyStatusUpdate{
		WALWritePosition: pglogrepl.LSN(lsn),
		WALFlushPosition: pglogrepl.LSN(lsn),
		WALApplyPosition: pglogrepl.LSN(lsn),
		ClientTime:       time.Now(),
		ReplyRequested:   reply,
	})
	if err != nil {
		return fmt.Errorf("cdc: send status update: %w", err)
	}
	r.lastMsg = time.Now()
	if lsn > r.sentAck {
		r.sentAck = lsn
		if r.OnAck != nil {
			r.OnAck(lsn)
		}
	}
	return nil
}

// FormatLSN renders an LSN the way Postgres does (16/B374D848).
func FormatLSN(lsn uint64) string { return pglogrepl.LSN(lsn).String() }

// ParseLSN parses a Postgres LSN string.
func ParseLSN(s string) (uint64, error) {
	l, err := pglogrepl.ParseLSN(s)
	return uint64(l), err
}
