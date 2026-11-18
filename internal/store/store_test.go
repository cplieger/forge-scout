package store_test

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/bolttable"
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
	mutes  *bolttable.Table[[]byte, placeholder]
	repos  *bolttable.Table[[]byte, placeholder]
}

func withTables(t *testing.T, ownerVersion, forgeVersion uint32) tables {
	t.Helper()
	s := newSchema(t)
	mutes, err := store.Define[[]byte, placeholder](s, mutesSpec(t), bolttable.BytesKey{}, ownerVersion)
	if err != nil {
		t.Fatalf("Define mutes: %v", err)
	}
	repos, err := store.Define[[]byte, placeholder](s, reposSpec(t), bolttable.BytesKey{}, forgeVersion)
	if err != nil {
		t.Fatalf("Define repositories: %v", err)
	}
	return tables{schema: s, mutes: mutes, repos: repos}
}

var (
	muteKey = []byte("mute")
	repoKey = []byte("repo")
)

// seed writes one row in each table of tb and closes the file.
func seed(t *testing.T, dir string, tb tables) {
	t.Helper()
	st, err := store.Open(dir, tb.schema, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	err = store.Live(st).Update(t.Context(), func(tx *bolttable.Tx) error {
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
	err := store.Live(st).View(t.Context(), func(r *bolttable.ReadTx) error {
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

func TestFailureAfterCloseKeepsTheOutage(t *testing.T) {
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
	st2, err := store.Open(dir, newSchema(t), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := st2.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := st2.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if class, failed := st2.Failure(); !failed || class != "newer" {
		t.Errorf("Failure() after Close = %q, %v; want newer, true", class, failed)
	}
}

func TestFailureDoesNotWaitForClose(t *testing.T) {
	t.Parallel()
	st := openStore(t, t.TempDir(), newSchema(t))
	entered, release := make(chan struct{}), make(chan struct{})
	viewed := make(chan error, 1)
	go func() {
		viewed <- store.Live(st).View(t.Context(), func(*bolttable.ReadTx) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	closed := make(chan error, 1)
	go func() { closed <- st.Close() }()
	for deadline := time.Now().Add(5 * time.Second); !errors.Is(store.Live(st).Err(), bolttable.ErrClosed); {
		if time.Now().After(deadline) {
			close(release)
			t.Fatal("Close did not start closing the handle")
		}
		time.Sleep(time.Millisecond)
	}
	answered := make(chan bool, 1)
	go func() {
		_, failed := st.Failure()
		answered <- failed
	}()
	select {
	case failed := <-answered:
		if failed {
			t.Error("Failure() during Close = failed, want none")
		}
	case <-time.After(5 * time.Second):
		t.Error("Failure() waited for Close to finish")
	}
	close(release)
	if err := <-viewed; err != nil {
		t.Errorf("View: %v", err)
	}
	if err := <-closed; err != nil {
		t.Errorf("Close: %v", err)
	}
}
