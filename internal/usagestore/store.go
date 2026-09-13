package usagestore

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"

	// Register the pure-Go SQLite driver under the name "sqlite".
	_ "modernc.org/sqlite"
)

const (
	// queueCapacity bounds the buffered channel between the request path and the writer.
	queueCapacity = 8192
	// batchSize flushes a transaction once this many rows are pending.
	batchSize = 200
	// flushInterval flushes a partially filled batch on this cadence.
	flushInterval = time.Second
	// dropLogInterval rate-limits the warning emitted when the queue overflows.
	dropLogInterval = time.Minute
	// pruneInterval is how often expired rows are deleted when retention is enabled.
	pruneInterval = time.Hour
)

// queueItem is either a row to persist or a flush barrier carrying an acknowledgement.
type queueItem struct {
	row *Row
	ack chan struct{}
}

// Options configures a Store.
type Options struct {
	// Path is the SQLite database file. It must already be tilde-expanded.
	Path string
	// RetentionDays prunes rows older than N days hourly. 0 keeps rows forever.
	RetentionDays int
}

// Store persists usage rows into SQLite from a single writer goroutine.
type Store struct {
	db            *sql.DB
	path          string
	retentionDays int

	queue chan queueItem
	done  chan struct{}

	cancel context.CancelFunc

	mu     sync.RWMutex
	closed bool

	dropped     atomic.Int64
	lastDropLog atomic.Int64
}

// Open creates the database file and its parent directory, applies the schema and starts
// the writer and retention goroutines.
func Open(opts Options) (*Store, error) {
	path := opts.Path
	if path == "" {
		return nil, fmt.Errorf("usagestore: empty database path")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if errMkdir := os.MkdirAll(dir, 0o755); errMkdir != nil {
			return nil, fmt.Errorf("usagestore: create directory %s: %w", dir, errMkdir)
		}
	}

	db, errOpen := sql.Open("sqlite", dsn(path))
	if errOpen != nil {
		return nil, fmt.Errorf("usagestore: open %s: %w", path, errOpen)
	}
	// SQLite serializes writers; a small pool keeps concurrent management reads cheap.
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	db.SetConnMaxIdleTime(5 * time.Minute)

	if errPing := db.Ping(); errPing != nil {
		closeQuietly(db)
		return nil, fmt.Errorf("usagestore: open %s: %w", path, errPing)
	}
	if _, errSchema := db.Exec(schemaSQL); errSchema != nil {
		closeQuietly(db)
		return nil, fmt.Errorf("usagestore: apply schema to %s: %w", path, errSchema)
	}

	retentionDays := opts.RetentionDays
	if retentionDays < 0 {
		retentionDays = 0
	}

	ctx, cancel := context.WithCancel(context.Background())
	s := &Store{
		db:            db,
		path:          path,
		retentionDays: retentionDays,
		queue:         make(chan queueItem, queueCapacity),
		done:          make(chan struct{}),
		cancel:        cancel,
	}
	go s.run()
	if retentionDays > 0 {
		go s.prune(ctx)
	}
	return s, nil
}

// dsn builds the SQLite connection string. WAL and busy_timeout are set per connection so
// every pooled connection gets them, not just the first.
func dsn(path string) string {
	return "file:" + url.PathEscape(path) +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_pragma=foreign_keys(1)"
}

// Path returns the database file path.
func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// RetentionDays returns the configured retention window in days.
func (s *Store) RetentionDays() int {
	if s == nil {
		return 0
	}
	return s.retentionDays
}

// DB exposes the underlying handle for read queries.
func (s *Store) DB() *sql.DB {
	if s == nil {
		return nil
	}
	return s.db
}

// Enqueue hands a row to the writer goroutine. It never blocks the request path: when the
// queue is full the row is dropped and a warning is logged at most once per minute.
func (s *Store) Enqueue(row *Row) {
	if s == nil || row == nil {
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return
	}
	select {
	case s.queue <- queueItem{row: row}:
	default:
		s.noteDrop()
	}
}

// Flush blocks until every record enqueued before the call has been written. It is used by
// tests and by callers that need the database to reflect a request immediately.
func (s *Store) Flush(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ack := make(chan struct{})
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return nil
	}
	select {
	case s.queue <- queueItem{ack: ack}:
		s.mu.RUnlock()
	case <-ctx.Done():
		s.mu.RUnlock()
		return ctx.Err()
	}
	select {
	case <-ack:
		return nil
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Store) noteDrop() {
	total := s.dropped.Add(1)
	now := time.Now().UnixNano()
	last := s.lastDropLog.Load()
	if now-last < int64(dropLogInterval) {
		return
	}
	if !s.lastDropLog.CompareAndSwap(last, now) {
		return
	}
	log.WithField("dropped_total", total).Warn("usage store queue full; dropping usage records")
}

// Dropped returns the number of records dropped because the queue was full.
func (s *Store) Dropped() int64 {
	if s == nil {
		return 0
	}
	return s.dropped.Load()
}

// Close stops the writer, flushes everything still queued and closes the database.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	close(s.queue)
	s.mu.Unlock()

	if s.cancel != nil {
		s.cancel()
	}
	<-s.done
	return s.db.Close()
}

func (s *Store) run() {
	defer close(s.done)
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

	batch := make([]*Row, 0, batchSize)
	for {
		select {
		case item, ok := <-s.queue:
			if !ok {
				s.flush(batch)
				return
			}
			if item.ack != nil {
				s.flush(batch)
				batch = batch[:0]
				close(item.ack)
				continue
			}
			batch = append(batch, item.row)
			if len(batch) >= batchSize {
				s.flush(batch)
				batch = batch[:0]
			}
		case <-ticker.C:
			if len(batch) > 0 {
				s.flush(batch)
				batch = batch[:0]
			}
		}
	}
}

// flush writes a batch inside one transaction. A failed batch is logged and discarded so
// the writer keeps draining the queue.
func (s *Store) flush(batch []*Row) {
	if len(batch) == 0 {
		return
	}
	if errInsert := s.insertBatch(batch); errInsert != nil {
		log.WithError(errInsert).WithField("rows", len(batch)).Error("usage store: failed to persist usage records")
	}
}

func (s *Store) insertBatch(batch []*Row) error {
	tx, errBegin := s.db.Begin()
	if errBegin != nil {
		return fmt.Errorf("begin transaction: %w", errBegin)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		if errRollback := tx.Rollback(); errRollback != nil && errRollback != sql.ErrTxDone {
			log.Errorf("usage store: rollback failed: %v", errRollback)
		}
	}()

	stmt, errPrepare := tx.Prepare(insertSQL)
	if errPrepare != nil {
		return fmt.Errorf("prepare insert: %w", errPrepare)
	}
	defer func() {
		if errClose := stmt.Close(); errClose != nil {
			log.Errorf("usage store: close statement failed: %v", errClose)
		}
	}()

	for _, row := range batch {
		if row == nil {
			continue
		}
		if _, errExec := stmt.Exec(row.args()...); errExec != nil {
			return fmt.Errorf("insert usage row: %w", errExec)
		}
	}
	if errCommit := tx.Commit(); errCommit != nil {
		return fmt.Errorf("commit transaction: %w", errCommit)
	}
	committed = true
	return nil
}

func (s *Store) prune(ctx context.Context) {
	ticker := time.NewTicker(pruneInterval)
	defer ticker.Stop()
	s.pruneOnce()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.pruneOnce()
		}
	}
}

// PruneOlderThan deletes rows with ts_ms older than the cutoff and returns the count.
func (s *Store) PruneOlderThan(cutoff time.Time) (int64, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	result, errExec := s.db.Exec("DELETE FROM usage_requests WHERE ts_ms < ?", cutoff.UnixMilli())
	if errExec != nil {
		return 0, fmt.Errorf("prune usage rows: %w", errExec)
	}
	affected, errAffected := result.RowsAffected()
	if errAffected != nil {
		return 0, nil
	}
	return affected, nil
}

func (s *Store) pruneOnce() {
	if s.retentionDays <= 0 {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -s.retentionDays)
	deleted, errPrune := s.PruneOlderThan(cutoff)
	if errPrune != nil {
		log.WithError(errPrune).Warn("usage store: retention prune failed")
		return
	}
	if deleted > 0 {
		log.WithFields(log.Fields{"deleted": deleted, "retention_days": s.retentionDays}).Info("usage store: pruned expired usage records")
	}
}

func closeQuietly(db *sql.DB) {
	if db == nil {
		return
	}
	if errClose := db.Close(); errClose != nil {
		log.Errorf("usage store: close database failed: %v", errClose)
	}
}
