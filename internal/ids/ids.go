// Package ids makes sortable, prefixed identifiers like "chg_01J9Z3...".
//
// The body is a ULID: 48 bits of milliseconds then 80 random bits, in
// Crockford base32. IDs sort by creation time and are safe in URLs.
package ids

import (
	"crypto/rand"
	"encoding/binary"
	"time"
)

const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// New returns prefix + "_" + a 26-char ULID.
func New(prefix string) string {
	return prefix + "_" + ulid(time.Now())
}

func ulid(t time.Time) string {
	var b [16]byte
	ms := uint64(t.UnixMilli())
	binary.BigEndian.PutUint16(b[0:2], uint16(ms>>32))
	binary.BigEndian.PutUint32(b[2:6], uint32(ms))
	_, _ = rand.Read(b[6:])
	// 128 bits -> 26 base32 chars (the first char carries 3 bits).
	var out [26]byte
	var acc uint64
	var bits uint
	i := 25
	for j := 15; j >= 0; j-- {
		acc |= uint64(b[j]) << bits
		bits += 8
		for bits >= 5 && i >= 0 {
			out[i] = alphabet[acc&31]
			acc >>= 5
			bits -= 5
			i--
		}
	}
	if i >= 0 {
		out[i] = alphabet[acc&31]
	}
	return string(out[:])
}
