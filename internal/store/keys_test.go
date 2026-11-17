package store_test

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/forge-scout/internal/forge"
	"github.com/cplieger/forge-scout/internal/store"
)

// longRepo is a repository id longer than one length byte can count.
var longRepo = "v1." + strings.Repeat("61", 200)

func TestRepoKeyCodecRoundTrip(t *testing.T) {
	t.Parallel()
	for _, k := range []forge.RepoKey{
		{Connection: "home", Repo: "v1.6f2f72"},
		{Connection: "", Repo: ""},
		{Connection: "home", Repo: longRepo},
	} {
		c := store.RepoKeyCodec{}
		got, err := c.ParseKey(c.AppendKey(nil, k))
		if err != nil || got != k {
			t.Errorf("ParseKey(AppendKey(%+v)) = %+v, %v", k, got, err)
		}
	}
}

func TestItemKeyCodecRoundTrip(t *testing.T) {
	t.Parallel()
	for _, k := range []forge.ItemKey{
		{Connection: "home", Repo: "v1.6f2f72", Number: 1},
		{Connection: "home", Repo: longRepo, Number: 1 << 40},
	} {
		c := store.ItemKeyCodec{}
		got, err := c.ParseKey(c.AppendKey(nil, k))
		if err != nil || got != k {
			t.Errorf("ParseKey(AppendKey(%+v)) = %+v, %v", k, got, err)
		}
	}
}

func TestItemKeysScanByNumber(t *testing.T) {
	t.Parallel()
	c := store.ItemKeyCodec{}
	numbers := []int{1, 2, 9, 10, 255, 256, 100000}
	keys := make([][]byte, 0, len(numbers))
	for _, n := range numbers {
		keys = append(keys, c.AppendKey(nil, forge.ItemKey{Connection: "home", Repo: "v1.6f2f72", Number: n}))
	}
	if !slices.IsSortedFunc(keys, bytes.Compare) {
		t.Errorf("keys of ascending numbers are not in byte order: %x", keys)
	}
}

func TestConnectionPrefix(t *testing.T) {
	t.Parallel()
	items := store.ItemKeyCodec{}
	repos := store.RepoKeyCodec{}
	prefix := store.ConnectionPrefix("a")
	for _, tc := range []struct {
		name string
		key  []byte
		in   bool
	}{
		{"own item", items.AppendKey(nil, forge.ItemKey{Connection: "a", Repo: "v1.72", Number: 3}), true},
		{"own repository", repos.AppendKey(nil, forge.RepoKey{Connection: "a", Repo: longRepo}), true},
		{"connection named with the id as its prefix", items.AppendKey(nil, forge.ItemKey{Connection: "ab", Repo: "v1.72", Number: 3}), false},
		{"connection whose length byte is the id's first byte", repos.AppendKey(nil, forge.RepoKey{Connection: "\x01a", Repo: "a"}), false},
		{"other connection", repos.AppendKey(nil, forge.RepoKey{Connection: "b", Repo: "v1.72"}), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := bytes.HasPrefix(tc.key, prefix); got != tc.in {
				t.Errorf("key %x under ConnectionPrefix(a) = %v, want %v", tc.key, got, tc.in)
			}
		})
	}
}

func TestParseKeyRefusals(t *testing.T) {
	t.Parallel()
	repo := store.RepoKeyCodec{}.AppendKey(nil, forge.RepoKey{Connection: "home", Repo: "v1.72"})
	item := store.ItemKeyCodec{}.AppendKey(nil, forge.ItemKey{Connection: "home", Repo: "v1.72", Number: 7})
	for _, tc := range []struct {
		name string
		b    []byte
	}{
		{"empty", nil},
		{"length past the end", []byte{0x09, 'a'}},
		{"unterminated uvarint", []byte{0x80}},
		{"overlong length", []byte{0x80, 0x00, 0x00}},
		{"repository id missing", []byte{0x01, 'a'}},
		{"repository key with trailing bytes", append(slices.Clone(repo), 0x00)},
		{"item key with a short number", item[:len(item)-1]},
		{"item key with a long number", append(slices.Clone(item), 0x00)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if k, err := (store.RepoKeyCodec{}).ParseKey(tc.b); err == nil {
				t.Errorf("RepoKeyCodec.ParseKey(%x) = %+v, want an error", tc.b, k)
			}
			if k, err := (store.ItemKeyCodec{}).ParseKey(tc.b); err == nil {
				t.Errorf("ItemKeyCodec.ParseKey(%x) = %+v, want an error", tc.b, k)
			}
		})
	}
	t.Run("an item key as a repository key", func(t *testing.T) {
		t.Parallel()
		if k, err := (store.RepoKeyCodec{}).ParseKey(item); err == nil {
			t.Errorf("RepoKeyCodec.ParseKey(%x) = %+v, want an error", item, k)
		}
	})
	t.Run("a repository key as an item key", func(t *testing.T) {
		t.Parallel()
		if k, err := (store.ItemKeyCodec{}).ParseKey(repo); err == nil {
			t.Errorf("ItemKeyCodec.ParseKey(%x) = %+v, want an error", repo, k)
		}
	})
}
