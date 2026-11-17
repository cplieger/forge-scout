package store_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cplieger/bolttable"
	"github.com/cplieger/forge-scout/internal/forge"
	"github.com/cplieger/forge-scout/internal/store"
)

// openStore opens dir's file under s, closed at cleanup.
func openStore(t *testing.T, dir string, s *store.Schema) *store.Store {
	t.Helper()
	return openLogged(t, dir, s, slog.New(slog.DiscardHandler))
}

func openLogged(t *testing.T, dir string, s *store.Schema, logger *slog.Logger) *store.Store {
	t.Helper()
	st, err := store.Open(dir, s, logger)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func newSchema(t *testing.T) *store.Schema {
	t.Helper()
	s, err := store.NewSchema()
	if err != nil {
		t.Fatalf("NewSchema: %v", err)
	}
	return s
}

// tables is a schema with one owner and one forge table at the given value
// versions.
type tables struct {
	schema *store.Schema
	mutes  *bolttable.Table[forge.ItemKey, placeholder]
	repos  *bolttable.Table[forge.RepoKey, placeholder]
}

func withTables(t *testing.T, ownerVersion, forgeVersion uint32) tables {
	t.Helper()
	s := newSchema(t)
	mutes, err := store.Define[forge.ItemKey, placeholder](s, store.Mutes, store.ItemKeyCodec{}, ownerVersion)
	if err != nil {
		t.Fatalf("Define mutes: %v", err)
	}
	repos, err := store.Define[forge.RepoKey, placeholder](s, store.Repositories, store.RepoKeyCodec{}, forgeVersion)
	if err != nil {
		t.Fatalf("Define repositories: %v", err)
	}
	return tables{schema: s, mutes: mutes, repos: repos}
}

var (
	muteKey = forge.ItemKey{Connection: "home", Repo: "v1.72", Number: 4}
	repoKey = forge.RepoKey{Connection: "home", Repo: "v1.72"}
)

// seed writes one row in each table of tb and closes the file.
func seed(t *testing.T, dir string, tb tables) {
	t.Helper()
	st, err := store.Open(dir, tb.schema, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	err = st.Update(t.Context(), func(tx *bolttable.Tx) error {
		if err := tb.mutes.Put(tx, muteKey, &placeholder{Value: "mute"}); err != nil {
			return err
		}
		return tb.repos.Put(tx, repoKey, &placeholder{Value: "repo"})
	})
	if err != nil {
		t.Fatalf("seed Update: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}

func wantFailure(t *testing.T, st *store.Store, want string) {
	t.Helper()
	class, failed := st.Failure()
	if !failed || class != want {
		t.Errorf("Failure() = %q, %v, want %q, true", class, failed, want)
	}
}

func TestNewFileStamped(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := newSchema(t)
	st := openStore(t, dir, s)
	if class, failed := st.Failure(); failed {
		t.Fatalf("Failure() = %q, want none", class)
	}
	v, found, err := store.Stamp(t.Context(), st, s)
	if err != nil || !found || v != store.StoreVersion {
		t.Errorf("Stamp = %d, %v, %v, want %d, true, nil", v, found, err, store.StoreVersion)
	}
	if _, err := os.Stat(filepath.Join(dir, "forge-scout.db")); err != nil {
		t.Errorf("data file: %v", err)
	}
}

func TestOlderStampRaised(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := newSchema(t)
	st, err := store.Open(dir, s, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := store.SetStamp(t.Context(), st, s, 0); err != nil {
		t.Fatalf("SetStamp: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s2 := newSchema(t)
	st2 := openStore(t, dir, s2)
	v, found, err := store.Stamp(t.Context(), st2, s2)
	if err != nil || !found || v != store.StoreVersion {
		t.Errorf("Stamp after reopen = %d, %v, %v, want %d, true, nil", v, found, err, store.StoreVersion)
	}
}

func TestNewerStampRefused(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := newSchema(t)
	st, err := store.Open(dir, s, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := store.SetStamp(t.Context(), st, s, store.StoreVersion+1); err != nil {
		t.Fatalf("SetStamp: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	path := filepath.Join(dir, "forge-scout.db")
	before := readFile(t, path)

	var logs bytes.Buffer
	st2 := openLogged(t, dir, newSchema(t), slog.New(slog.NewJSONHandler(&logs, nil)))
	wantFailure(t, st2, "newer")
	if !bytes.Equal(readFile(t, path), before) {
		t.Error("the refused file changed")
	}
	line := logs.String()
	for _, want := range []string{`"level":"ERROR"`, `"class":"newer"`, `"file":"` + path + `"`} {
		if !strings.Contains(line, want) {
			t.Errorf("open log %s lacks %s", line, want)
		}
	}
}

func TestOutageRefusesCalls(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "forge-scout.db"), bytes.Repeat([]byte{0xAB}, 64<<10), 0o600); err != nil {
		t.Fatal(err)
	}
	st := openStore(t, dir, newSchema(t))
	if err := st.View(t.Context(), func(*bolttable.ReadTx) error { return nil }); !errors.Is(err, bolttable.ErrClosed) {
		t.Errorf("View in the outage = %v, want ErrClosed", err)
	}
	if err := st.Update(t.Context(), func(*bolttable.Tx) error { return nil }); !errors.Is(err, bolttable.ErrClosed) {
		t.Errorf("Update in the outage = %v, want ErrClosed", err)
	}
	if st.WriteFailing() {
		t.Error("WriteFailing() in the outage = true, want false: no commit ran")
	}
}

func TestDamagedFileLeftInPlace(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "forge-scout.db")
	garbage := bytes.Repeat([]byte{0xAB}, 64<<10)
	if err := os.WriteFile(path, garbage, 0o600); err != nil {
		t.Fatal(err)
	}
	st := openStore(t, dir, newSchema(t))
	wantFailure(t, st, "damaged")
	if !bytes.Equal(readFile(t, path), garbage) {
		t.Error("the damaged file changed")
	}
	if moved, _ := filepath.Glob(filepath.Join(dir, "*.invalid")); len(moved) != 0 {
		t.Errorf("a copy was set aside: %v", moved)
	}
}

func TestOwnerFormatChangeRefused(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	seed(t, dir, withTables(t, 1, 1))
	path := filepath.Join(dir, "forge-scout.db")
	before := readFile(t, path)
	st := openStore(t, dir, withTables(t, 2, 1).schema)
	wantFailure(t, st, "format")
	if !bytes.Equal(readFile(t, path), before) {
		t.Error("the refused file changed")
	}
}

func TestForgeFormatChangeResetsForgeAlone(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	seed(t, dir, withTables(t, 1, 1))
	tb := withTables(t, 1, 2)
	var logs bytes.Buffer
	st := openLogged(t, dir, tb.schema, slog.New(slog.NewJSONHandler(&logs, nil)))
	if class, failed := st.Failure(); failed {
		t.Fatalf("Failure() = %q, want none", class)
	}
	err := st.View(t.Context(), func(r *bolttable.ReadTx) error {
		if _, found, err := tb.mutes.Get(r, muteKey); err != nil || !found {
			t.Errorf("owner row after the reset: found %v, %v; want kept", found, err)
		}
		if _, found, err := tb.repos.Get(r, repoKey); err != nil || found {
			t.Errorf("forge row after the reset: found %v, %v; want gone", found, err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if kept, _ := filepath.Glob(filepath.Join(dir, "forge-scout.db.*.old")); len(kept) != 1 {
		t.Errorf("copies kept before the reset = %v, want one", kept)
	}
	if !strings.Contains(logs.String(), `"namespaces":["forge"]`) {
		t.Errorf("open log %s does not name the reset namespace", logs.String())
	}
}

// TestSecondOpenLocked runs serially: it shortens the lock wait.
func TestSecondOpenLocked(t *testing.T) {
	store.SetLockWait(t, 0)
	dir := t.TempDir()
	openStore(t, dir, newSchema(t))
	st, err := store.Open(dir, newSchema(t), slog.New(slog.DiscardHandler))
	if !errors.Is(err, bolttable.ErrLocked) {
		t.Errorf("second Open = %v, %v, want ErrLocked", st, err)
	}
	if err != nil && !strings.Contains(err.Error(), filepath.Join(dir, "forge-scout.db")) {
		t.Errorf("second Open's error %q does not name the file", err)
	}
}

// TestWriteFailing runs serially: it sets the commit fault.
func TestWriteFailing(t *testing.T) {
	var fail atomic.Bool
	store.SetCommitFault(t, func(context.Context) error {
		if fail.Load() {
			return errors.New("disk full")
		}
		return nil
	})
	tb := withTables(t, 1, 1)
	st := openStore(t, t.TempDir(), tb.schema)
	n := 0
	put := func() error {
		n++
		return st.Update(t.Context(), func(tx *bolttable.Tx) error {
			return tb.repos.Put(tx, repoKey, &placeholder{Value: strconv.Itoa(n)})
		})
	}
	if err := put(); err != nil || st.WriteFailing() {
		t.Fatalf("healthy Update = %v, WriteFailing %v; want nil, false", err, st.WriteFailing())
	}
	fail.Store(true)
	if err := put(); !errors.Is(err, bolttable.ErrCommit) {
		t.Errorf("faulted Update = %v, want ErrCommit", err)
	}
	if !st.WriteFailing() {
		t.Error("WriteFailing() after a failed commit = false, want true")
	}
	if err := st.Update(t.Context(), func(*bolttable.Tx) error { return errors.New("refused") }); err == nil {
		t.Error("Update returning its own error succeeded")
	}
	if !st.WriteFailing() {
		t.Error("WriteFailing() after the caller's own error = false, want still true")
	}
	fail.Store(false)
	if err := put(); err != nil {
		t.Errorf("recovered Update = %v", err)
	}
	if st.WriteFailing() {
		t.Error("WriteFailing() after a commit = true, want false")
	}
}

func TestFailureAfterClose(t *testing.T) {
	t.Parallel()
	st, err := store.Open(t.TempDir(), newSchema(t), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if class, failed := st.Failure(); failed {
		t.Errorf("Failure() after Close = %q, true; want none", class)
	}
}

func TestLiveIsTheOpenHandle(t *testing.T) {
	t.Parallel()
	st := openStore(t, t.TempDir(), newSchema(t))
	if err := st.Live().Err(); err != nil {
		t.Errorf("Live().Err() = %v, want nil on an open store", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := st.Live().Err(); !errors.Is(err, bolttable.ErrClosed) {
		t.Errorf("Live().Err() after Close = %v, want ErrClosed", err)
	}
}
