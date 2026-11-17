package store_test

import (
	"bytes"
	"testing"

	"github.com/cplieger/forge-scout/internal/forge"
	"github.com/cplieger/forge-scout/internal/store"
)

// FuzzKeyCodecs_canonical holds both codecs to one encoding per key: any
// bytes ParseKey accepts are exactly what AppendKey writes for its answer,
// so a stored key scans and deletes under the key it decodes to.
func FuzzKeyCodecs_canonical(f *testing.F) {
	f.Add(store.ItemKeyCodec{}.AppendKey(nil, forge.ItemKey{Connection: "home", Repo: "v1.72", Number: 7}))
	f.Add(store.RepoKeyCodec{}.AppendKey(nil, forge.RepoKey{Connection: "home", Repo: longRepo}))
	f.Add([]byte{0x80, 0x00, 0x00})
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) {
		if k, err := (store.RepoKeyCodec{}).ParseKey(b); err == nil {
			if again := (store.RepoKeyCodec{}).AppendKey(nil, k); !bytes.Equal(again, b) {
				t.Errorf("RepoKeyCodec: %x parsed to %+v, which encodes as %x", b, k, again)
			}
		}
		if k, err := (store.ItemKeyCodec{}).ParseKey(b); err == nil {
			if again := (store.ItemKeyCodec{}).AppendKey(nil, k); !bytes.Equal(again, b) {
				t.Errorf("ItemKeyCodec: %x parsed to %+v, which encodes as %x", b, k, again)
			}
		}
	})
}
