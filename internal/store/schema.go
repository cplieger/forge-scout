package store

import (
	"fmt"

	"github.com/cplieger/bolttable"
)

// schemaName enters every namespace's format digest.
const schemaName = "forge-scout"

// StoreVersion is this build's store version stamp. A file stamped higher
// was written by a newer build and refuses to open as "newer"; raise it
// when a build writes what an older build would misread under an unchanged
// format digest.
const StoreVersion uint32 = 1

// metaKey is the key of the one meta row.
var metaKey = []byte("store")

// meta is the meta row: the stamp of the newest build that opened the file.
type meta struct {
	StoreVersion uint32 `json:"store_version"`
}

// Schema is the file's namespaces and tables. Each slice that owns a table
// declares it through Define before Open.
type Schema struct {
	bt    *bolttable.Schema
	ns    map[string]*bolttable.Namespace
	class map[string]NamespaceClass
	meta  *bolttable.Table[[]byte, meta]
}

// NewSchema returns the schema with every namespace and the meta table.
func NewSchema() (*Schema, error) {
	s := &Schema{
		bt:    bolttable.NewSchema(schemaName),
		ns:    map[string]*bolttable.Namespace{},
		class: map[string]NamespaceClass{},
	}
	for _, spec := range Namespaces() {
		ns, err := s.bt.Namespace(spec.Name)
		if err != nil {
			return nil, err
		}
		s.ns[spec.Name], s.class[spec.Name] = ns, spec.Class
	}
	m, err := define(s, Meta, bolttable.BytesKey{}, bolttable.JSON[meta](1))
	if err != nil {
		return nil, err
	}
	s.meta = m
	return s, nil
}

// Define declares the table spec names with key codec k and values encoded
// as JSON at version, which enters the namespace's format digest: raise it
// when the encoding changes. It fails for a namespace the file does not
// have, a prefix or name already declared, and after Open.
func Define[K, V any](s *Schema, spec TableSpec, k bolttable.KeyCodec[K], version uint32) (*bolttable.Table[K, V], error) {
	return define(s, spec, k, bolttable.JSON[V](version))
}

func define[K, V any](s *Schema, spec TableSpec, k bolttable.KeyCodec[K], v bolttable.Codec[V]) (*bolttable.Table[K, V], error) {
	ns, ok := s.ns[spec.Namespace.Name]
	if !ok || s.class[spec.Namespace.Name] != spec.Namespace.Class {
		return nil, fmt.Errorf("store: table %s names no namespace of the file", spec.Name)
	}
	return bolttable.Define(ns, spec.Prefix, spec.Name, k, v, bolttable.TableOptions{Silent: spec.Silent})
}

// Prefixes returns the prefix of every non-silent table declared, ascending.
func (s *Schema) Prefixes() []byte { return s.bt.Prefixes() }
