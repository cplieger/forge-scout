package store

import (
	"context"
	"testing"
	"time"

	"github.com/cplieger/bolttable"
)

// SetLockWait sets Open's lock wait for one test, which must not run in
// parallel.
func SetLockWait(t *testing.T, d time.Duration) {
	t.Helper()
	old := lockWait
	lockWait = d
	t.Cleanup(func() { lockWait = old })
}

// SetCommitFault sets Options.CommitFault for the Opens of one test, which
// must not run in parallel.
func SetCommitFault(t *testing.T, fn func(context.Context) error) {
	t.Helper()
	old := commitFault
	commitFault = fn
	t.Cleanup(func() { commitFault = old })
}

// Stamp reads the file's store version stamp; found is false when it has
// none.
func Stamp(ctx context.Context, st *Store, s *Schema) (v uint32, found bool, err error) {
	err = st.View(ctx, func(r *bolttable.ReadTx) error {
		m, ok, gerr := s.meta.Get(r, metaKey)
		v, found = m.StoreVersion, ok
		return gerr
	})
	return v, found, err
}

// SetStamp writes v as the file's store version stamp.
func SetStamp(ctx context.Context, st *Store, s *Schema, v uint32) error {
	return st.Update(ctx, func(tx *bolttable.Tx) error {
		return s.meta.Put(tx, metaKey, &meta{StoreVersion: v})
	})
}
