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

// Live is st's handle.
func Live(st *Store) *bolttable.Live { return st.live }

// Define declares spec in s with key codec k and JSON values at version.
func Define[K, V any](s *Schema, spec TableSpec, k bolttable.KeyCodec[K], version uint32) (*bolttable.Table[K, V], error) {
	return define(s, spec, k, bolttable.JSON[V](version))
}

// Stamp reads the file's store version stamp; found is false when it has
// none.
func Stamp(ctx context.Context, st *Store, s *Schema) (v uint32, found bool, err error) {
	err = st.live.View(ctx, func(r *bolttable.ReadTx) error {
		m, ok, gerr := s.meta.Get(r, metaKey)
		v, found = m.StoreVersion, ok
		return gerr
	})
	return v, found, err
}

// SetStamp writes v as the file's store version stamp.
func SetStamp(ctx context.Context, st *Store, s *Schema, v uint32) error {
	return st.live.Update(ctx, func(tx *bolttable.Tx) error {
		return s.meta.Put(tx, metaKey, &meta{StoreVersion: v})
	})
}
