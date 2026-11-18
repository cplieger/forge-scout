// Package store is forge-scout's product file, forge-scout.db: its
// namespaces, the meta table, and the open policy on one bolttable handle.
package store

// NamespaceClass says what a format change in a namespace does.
type NamespaceClass uint8

// The NamespaceClass members.
const (
	// Durable holds what only the owner, or time, created; a format change
	// refuses the open, so the file is never reset.
	Durable NamespaceClass = iota + 1
	// Derived holds what the forges hold now; a format change resets it and
	// the next poll reads it back.
	Derived
)

// NamespaceSpec names one namespace and its class.
type NamespaceSpec struct {
	Name  string
	Class NamespaceClass
}

// TableSpec is one table's place in the file: its namespace, its name and
// its prefix byte, unique across the file. A Silent table's writes record
// no subject in the change feed.
type TableSpec struct {
	Name      string
	Namespace NamespaceSpec
	Prefix    byte
	Silent    bool
}

// The namespaces of the file.
var (
	nsOwner   = NamespaceSpec{Name: "owner", Class: Durable}
	nsForge   = NamespaceSpec{Name: "forge", Class: Derived}
	nsHistory = NamespaceSpec{Name: "history", Class: Durable}
)

// Meta is the table of the one meta row. Prefixes are grouped by namespace:
// owner 0x01 to 0x1F, forge 0x20 to 0x3F, history from 0x40. A prefix is
// never reused for another table, since a stored file may still hold its
// rows.
var Meta = TableSpec{Namespace: nsOwner, Name: "meta", Prefix: 0x01, Silent: true}

// Namespaces lists every namespace of the file.
func Namespaces() []NamespaceSpec { return []NamespaceSpec{nsOwner, nsForge, nsHistory} }
