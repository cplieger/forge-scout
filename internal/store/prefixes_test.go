package store_test

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/cplieger/bolttable"
	"github.com/cplieger/forge-scout/internal/store"
)

// placeholder is a row type standing in for the tables' own, which each
// slice declares in its stage.
type placeholder struct {
	Value string `json:"value"`
}

func TestNamespaces(t *testing.T) {
	t.Parallel()
	want := map[string]store.NamespaceClass{"owner": store.Durable, "forge": store.Derived, "history": store.Durable}
	got := map[string]store.NamespaceClass{}
	for _, ns := range store.Namespaces() {
		got[ns.Name] = ns.Class
	}
	if len(got) != len(want) {
		t.Errorf("Namespaces() = %v, want %v", got, want)
	}
	for name, class := range want {
		if got[name] != class {
			t.Errorf("namespace %s class = %d, want %d", name, got[name], class)
		}
	}
}

func TestTablesPlacement(t *testing.T) {
	t.Parallel()
	ranges := map[string][2]byte{"owner": {0x01, 0x1F}, "forge": {0x20, 0x3F}, "history": {0x40, 0xFE}}
	prefixes := map[byte]string{}
	names := map[string]bool{}
	for _, tb := range store.Tables() {
		if other, dup := prefixes[tb.Prefix]; dup {
			t.Errorf("prefix %#x of %s already taken by %s", tb.Prefix, tb.Name, other)
		}
		prefixes[tb.Prefix] = tb.Name
		q := tb.Namespace.Name + "." + tb.Name
		if names[q] {
			t.Errorf("table %s listed twice", q)
		}
		names[q] = true
		r, ok := ranges[tb.Namespace.Name]
		if !ok {
			t.Errorf("table %s in namespace %q, not one of the file's", tb.Name, tb.Namespace.Name)
			continue
		}
		if tb.Prefix < r[0] || tb.Prefix > r[1] {
			t.Errorf("table %s prefix %#x outside its namespace's %#x to %#x", q, tb.Prefix, r[0], r[1])
		}
	}
}

// TestSilentTables pins which tables record no change-feed subject: a table
// with no change kind must be silent, or the change feed refuses the schema.
func TestSilentTables(t *testing.T) {
	t.Parallel()
	want := []string{"credentials", "disabled_seen", "held", "meta", "removals", "repo_reads", "write_times"}
	var got []string
	for _, tb := range store.Tables() {
		if tb.Silent {
			got = append(got, tb.Name)
		}
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("silent tables = %v, want %v", got, want)
	}
}

// fullSchema declares every table but Meta, which NewSchema declares, over
// placeholder rows.
func fullSchema(t *testing.T) *store.Schema {
	t.Helper()
	s, err := store.NewSchema()
	if err != nil {
		t.Fatalf("NewSchema: %v", err)
	}
	for _, tb := range store.Tables() {
		if tb == store.Meta {
			continue
		}
		if _, err := store.Define[[]byte, placeholder](s, tb, bolttable.BytesKey{}, 1); err != nil {
			t.Fatalf("Define %s: %v", tb.Name, err)
		}
	}
	return s
}

func TestFullSchemaOpens(t *testing.T) {
	t.Parallel()
	s := fullSchema(t)
	st := openStore(t, t.TempDir(), s)
	if class, failed := st.Failure(); failed {
		t.Fatalf("Failure() = %q, want none", class)
	}
	var want []byte
	for _, tb := range store.Tables() {
		if !tb.Silent {
			want = append(want, tb.Prefix)
		}
	}
	slices.Sort(want)
	if got := s.Prefixes(); !slices.Equal(got, want) {
		t.Errorf("Prefixes() = %x, want %x", got, want)
	}
}

func TestDefineRefusals(t *testing.T) {
	t.Parallel()
	t.Run("prefix collision", func(t *testing.T) {
		t.Parallel()
		s := fullSchema(t)
		clash := store.TableSpec{Namespace: store.NSForge, Name: "clash", Prefix: store.Repositories.Prefix}
		if _, err := store.Define[[]byte, placeholder](s, clash, bolttable.BytesKey{}, 1); err == nil {
			t.Error("Define of a taken prefix succeeded")
		}
	})
	t.Run("meta declared twice", func(t *testing.T) {
		t.Parallel()
		s, err := store.NewSchema()
		if err != nil {
			t.Fatalf("NewSchema: %v", err)
		}
		if _, err := store.Define[[]byte, placeholder](s, store.Meta, bolttable.BytesKey{}, 1); err == nil {
			t.Error("Define of Meta succeeded")
		}
	})
	t.Run("unknown namespace", func(t *testing.T) {
		t.Parallel()
		s, err := store.NewSchema()
		if err != nil {
			t.Fatalf("NewSchema: %v", err)
		}
		other := store.TableSpec{Namespace: store.NamespaceSpec{Name: "other", Class: store.Durable}, Name: "rows", Prefix: 0x50}
		if _, err := store.Define[[]byte, placeholder](s, other, bolttable.BytesKey{}, 1); err == nil {
			t.Error("Define in an undeclared namespace succeeded")
		}
	})
	t.Run("namespace with another class", func(t *testing.T) {
		t.Parallel()
		s, err := store.NewSchema()
		if err != nil {
			t.Fatalf("NewSchema: %v", err)
		}
		spec := store.Mutes
		spec.Namespace.Class = store.Derived
		if _, err := store.Define[[]byte, placeholder](s, spec, bolttable.BytesKey{}, 1); err == nil {
			t.Error("Define with the owner namespace marked derived succeeded")
		}
	})
	t.Run("after open", func(t *testing.T) {
		t.Parallel()
		s, err := store.NewSchema()
		if err != nil {
			t.Fatalf("NewSchema: %v", err)
		}
		openStore(t, t.TempDir(), s)
		if _, err := store.Define[[]byte, placeholder](s, store.Mutes, bolttable.BytesKey{}, 1); err == nil {
			t.Error("Define after Open succeeded")
		}
	})
}

func TestPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, err := store.NewSchema()
	if err != nil {
		t.Fatalf("NewSchema: %v", err)
	}
	st := openStore(t, dir, s)
	if got, want := st.Path(), filepath.Join(dir, "forge-scout.db"); got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}
}
