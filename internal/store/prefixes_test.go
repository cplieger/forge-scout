package store_test

import (
	"path/filepath"
	"testing"

	"github.com/cplieger/bolttable"
	"github.com/cplieger/forge-scout/internal/store"
)

// placeholder is a row type standing in for the tables' own, which each
// slice declares in its stage.
type placeholder struct {
	Value string `json:"value"`
}

// namespace is the file's namespace named name.
func namespace(t *testing.T, name string) store.NamespaceSpec {
	t.Helper()
	for _, ns := range store.Namespaces() {
		if ns.Name == name {
			return ns
		}
	}
	t.Fatalf("no namespace %s", name)
	return store.NamespaceSpec{}
}

// mutesSpec and reposSpec stand in for one owner and one forge table.
func mutesSpec(t *testing.T) store.TableSpec {
	t.Helper()
	return store.TableSpec{Namespace: namespace(t, "owner"), Name: "mutes", Prefix: 0x03}
}

func reposSpec(t *testing.T) store.TableSpec {
	t.Helper()
	return store.TableSpec{Namespace: namespace(t, "forge"), Name: "repositories", Prefix: 0x20}
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

func TestDefineRefusals(t *testing.T) {
	t.Parallel()
	t.Run("prefix collision", func(t *testing.T) {
		t.Parallel()
		s := newSchema(t)
		if _, err := store.Define[[]byte, placeholder](s, reposSpec(t), bolttable.BytesKey{}, 1); err != nil {
			t.Fatalf("Define repositories: %v", err)
		}
		clash := store.TableSpec{Namespace: namespace(t, "forge"), Name: "clash", Prefix: reposSpec(t).Prefix}
		if _, err := store.Define[[]byte, placeholder](s, clash, bolttable.BytesKey{}, 1); err == nil {
			t.Error("Define of a taken prefix succeeded")
		}
	})
	t.Run("meta declared twice", func(t *testing.T) {
		t.Parallel()
		if _, err := store.Define[[]byte, placeholder](newSchema(t), store.Meta, bolttable.BytesKey{}, 1); err == nil {
			t.Error("Define of Meta succeeded")
		}
	})
	t.Run("unknown namespace", func(t *testing.T) {
		t.Parallel()
		other := store.TableSpec{Namespace: store.NamespaceSpec{Name: "other", Class: store.Durable}, Name: "rows", Prefix: 0x50}
		if _, err := store.Define[[]byte, placeholder](newSchema(t), other, bolttable.BytesKey{}, 1); err == nil {
			t.Error("Define in an undeclared namespace succeeded")
		}
	})
	t.Run("namespace with another class", func(t *testing.T) {
		t.Parallel()
		spec := mutesSpec(t)
		spec.Namespace.Class = store.Derived
		if _, err := store.Define[[]byte, placeholder](newSchema(t), spec, bolttable.BytesKey{}, 1); err == nil {
			t.Error("Define with the owner namespace marked derived succeeded")
		}
	})
	t.Run("after open", func(t *testing.T) {
		t.Parallel()
		s := newSchema(t)
		openStore(t, t.TempDir(), s)
		if _, err := store.Define[[]byte, placeholder](s, mutesSpec(t), bolttable.BytesKey{}, 1); err == nil {
			t.Error("Define after Open succeeded")
		}
	})
}

func TestPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	st := openStore(t, dir, newSchema(t))
	if got, want := st.Path(), filepath.Join(dir, "forge-scout.db"); got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}
}
