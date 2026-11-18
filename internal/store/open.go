package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/cplieger/bolttable"
)

// FileName is the product file's name in the data directory.
const FileName = "forge-scout.db"

// writeClass is the one WriteHealth class: every write is the product's.
const writeClass = "write"

// lockWait is how long Open waits for another holder of the file's lock,
// the window a restarted container's old process has to exit.
var lockWait = 5 * time.Second

// commitFault is Options.CommitFault, nil outside tests.
var commitFault func(context.Context) error

// Store is the product file on one bolttable.Live handle. A file that would
// not open leaves the Store in its outage: every call answers an error
// wrapping bolttable.ErrClosed, and Failure names the class.
type Store struct {
	live *bolttable.Live
	// closedErr is live's Err when Close ran, since Live.Close replaces it
	// with ErrClosed whatever the file's state was.
	closedErr error
	path      string
	health    bolttable.WriteHealth
	mu        sync.Mutex
	closed    bool
}

// Open opens <dataDir>/forge-scout.db under s, holding its lock until
// Close. It fails only when the handle cannot be made, a lock another
// process holds (bolttable.ErrLocked) among the causes; a file that does
// not open is logged at ERROR and the Store is returned in its outage. A
// format change resets a Derived namespace, keeping one .old copy of the
// file, and refuses the open for a Durable one; a damaged file is left in
// place.
func Open(dataDir string, s *Schema, logger *slog.Logger) (*Store, error) {
	path := filepath.Join(dataDir, FileName)
	live, err := bolttable.NewLive(path, s.bt, bolttable.Options{
		Inspect:     s.inspect,
		Reset:       bolttable.ResetPolicy{Allow: func(d bolttable.NamespaceDigest) bool { return s.class[d.Name] == Derived }, Keep: 1},
		LockWait:    lockWait,
		InvalidFile: bolttable.InvalidFileFail,
		CommitFault: commitFault,
	})
	if err != nil {
		return nil, fmt.Errorf("store: %s: %w", path, err)
	}
	st := &Store{live: live, path: path}
	live.OnOpen(func(db *bolttable.DB) error { return s.opened(db, logger) })
	if err := live.Open(); err != nil {
		class, _ := bolttable.OpenFailure(err)
		logger.Error("data file cannot be opened", "file", path, "class", class, "error", err)
	}
	return st, nil
}

// inspect refuses a file stamped by a newer build.
func (s *Schema) inspect(r *bolttable.ReadTx) error {
	m, found, err := s.meta.Get(r, metaKey)
	if err != nil {
		return err
	}
	if found && m.StoreVersion > StoreVersion {
		return fmt.Errorf("%w: store version %d, this build's is %d", bolttable.ErrNewerBuild, m.StoreVersion, StoreVersion)
	}
	return nil
}

// opened logs a reset the open made and stamps the file with StoreVersion.
func (s *Schema) opened(db *bolttable.DB, logger *slog.Logger) error {
	if r := db.OpenReport(); len(r.Reset) > 0 {
		names := make([]string, 0, len(r.Reset))
		for _, d := range r.Reset {
			names = append(names, d.Name)
		}
		logger.Info("data file namespaces reset on a format change", "namespaces", names, "kept", r.Kept)
	}
	return db.Update(context.Background(), func(tx *bolttable.Tx) error {
		m, found, err := s.meta.Get(tx.Read(), metaKey)
		if err != nil || (found && m.StoreVersion >= StoreVersion) {
			return err
		}
		return s.meta.Put(tx, metaKey, &meta{StoreVersion: StoreVersion})
	})
}

// Path is the product file's path.
func (st *Store) Path() string { return st.path }

// Failure is the outage class of a file that would not open (one of
// bolttable.OpenFailure's), and whether it failed. After Close it reports
// the state the file had when it closed.
func (st *Store) Failure() (class string, failed bool) {
	err := st.live.Err()
	if errors.Is(err, bolttable.ErrClosed) {
		st.mu.Lock()
		err = st.closedErr
		st.mu.Unlock()
	}
	return bolttable.OpenFailure(err)
}

// View runs fn in a read transaction.
func (st *Store) View(ctx context.Context, fn func(*bolttable.ReadTx) error) error {
	return st.live.View(ctx, fn)
}

// Update runs fn in a write transaction and records its commit's outcome
// for WriteFailing.
func (st *Store) Update(ctx context.Context, fn func(*bolttable.Tx) error) error {
	err := st.live.Update(ctx, fn)
	st.health.Record(writeClass, err)
	return err
}

// WriteFailing reports that the latest commit failed.
func (st *Store) WriteFailing() bool {
	rec, _ := st.health.Class(writeClass)
	return rec.Failing
}

// Live is the handle every store of the file shares, for a Swap that
// replaces the file under all of them.
func (st *Store) Live() *bolttable.Live { return st.live }

// Close closes the file and releases its lock. A second Close does nothing.
func (st *Store) Close() error {
	st.mu.Lock()
	defer st.mu.Unlock()
	if !st.closed {
		st.closedErr, st.closed = st.live.Err(), true
	}
	return st.live.Close()
}
