package store

import (
	"encoding/binary"
	"errors"

	"github.com/cplieger/forge-scout/internal/forge"
)

var errKey = errors.New("store: malformed key")

// RepoKeyCodec lays a forge.RepoKey out as the connection id, then the
// repository id, each after its byte length as a uvarint (a repository id
// can exceed 255 bytes). The lengths keep any id's encoding from being a
// prefix of another's, so one connection's rows are one range
// (ConnectionPrefix) and one repository's another.
type RepoKeyCodec struct{}

// ItemKeyCodec lays a forge.ItemKey out as its repository's key, then the
// number as 8 big-endian bytes, so a repository's items scan by ascending
// number; item numbers are positive.
type ItemKeyCodec struct{}

// Version is the layout's version.
func (RepoKeyCodec) Version() uint32 { return 1 }

// AppendKey appends k's key to dst.
func (RepoKeyCodec) AppendKey(dst []byte, k forge.RepoKey) []byte {
	return appendString(appendString(dst, k.Connection), k.Repo)
}

// ParseKey reads a key AppendKey wrote; any other bytes are an error.
func (RepoKeyCodec) ParseKey(b []byte) (forge.RepoKey, error) {
	k, rest, err := parseRepoKey(b)
	if err == nil && len(rest) != 0 {
		err = errKey
	}
	return k, err
}

// Version is the layout's version.
func (ItemKeyCodec) Version() uint32 { return 1 }

// AppendKey appends k's key to dst.
func (ItemKeyCodec) AppendKey(dst []byte, k forge.ItemKey) []byte {
	dst = RepoKeyCodec{}.AppendKey(dst, k.RepoKey())
	return binary.BigEndian.AppendUint64(dst, uint64(k.Number)) //nolint:gosec // G115: item numbers are positive
}

// ParseKey reads a key AppendKey wrote; any other bytes are an error.
func (ItemKeyCodec) ParseKey(b []byte) (forge.ItemKey, error) {
	rk, rest, err := parseRepoKey(b)
	if err != nil {
		return forge.ItemKey{}, err
	}
	if len(rest) != 8 {
		return forge.ItemKey{}, errKey
	}
	n := int(binary.BigEndian.Uint64(rest)) //nolint:gosec // G115: the inverse of AppendKey's conversion
	return forge.ItemKey{Connection: rk.Connection, Repo: rk.Repo, Number: n}, nil
}

// ConnectionPrefix is the key prefix of every row of the connection id,
// under RepoKeyCodec and ItemKeyCodec alike, and of no other connection's.
func ConnectionPrefix(id string) []byte { return appendString(nil, id) }

func appendString(dst []byte, s string) []byte {
	return append(binary.AppendUvarint(dst, uint64(len(s))), s...)
}

func parseRepoKey(b []byte) (k forge.RepoKey, rest []byte, err error) {
	conn, rest, err := parseString(b)
	if err != nil {
		return k, nil, err
	}
	repo, rest, err := parseString(rest)
	if err != nil {
		return k, nil, err
	}
	return forge.RepoKey{Connection: conn, Repo: repo}, rest, nil
}

func parseString(b []byte) (s string, rest []byte, err error) {
	n, w := binary.Uvarint(b)
	// An overlong uvarint decodes too; refusing it keeps one encoding per
	// key, so a row is always found under the key it decodes to.
	if w <= 0 || w != len(binary.AppendUvarint(nil, n)) || n > uint64(len(b[w:])) {
		return "", nil, errKey
	}
	end := w + int(n) //nolint:gosec // G115: n is at most len(b)
	return string(b[w:end]), b[end:], nil
}
