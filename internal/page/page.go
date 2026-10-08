// Package page is how list endpoints page: keyset (seek) pagination with an
// opaque cursor, never OFFSET. A list answers one Page; when more rows
// follow, NextCursor is set and the caller passes it back as cursor.
//
// Stores fetch limit+1 rows in a stable order (a unique key last, e.g.
// created_at DESC, id DESC) after the cursor's position, and Make trims the
// extra row and turns the last kept row's sort key into the next cursor.
package page

import (
	"encoding/base64"
	"errors"
	"strings"
)

const (
	// DefaultLimit is a page's size when the caller doesn't say.
	DefaultLimit = 50
	// MaxLimit is the largest page a list returns.
	MaxLimit = 200
)

// Page is one page of a list.
type Page[T any] struct {
	Items      []T    `json:"items" nullable:"false" doc:"This page, in the list's order"`
	NextCursor string `json:"nextCursor,omitempty" doc:"Set when more follow: pass it as cursor to read the next page. Absent on the last page."`
}

// Params is the paging part of a list's query; embed it in an input struct.
type Params struct {
	Limit  int    `query:"limit" minimum:"1" maximum:"200" default:"50" doc:"How many to return per page (at most 200)"`
	Cursor string `query:"cursor" maxLength:"512" doc:"The nextCursor of the previous page, to read the next one. Leave empty for the first page."`
}

// ErrBadCursor is a cursor this list didn't make (or made for other filters' order).
var ErrBadCursor = errors.New("cursor is not one this list returned; pass nextCursor from the previous page unchanged, or leave cursor empty for the first page")

// Clamp keeps a page's size between 1 and MaxLimit; 0 or less means DefaultLimit.
func Clamp(limit int) int {
	switch {
	case limit <= 0:
		return DefaultLimit
	case limit > MaxLimit:
		return MaxLimit
	}
	return limit
}

const sep = "\x1f"

// Encode makes an opaque cursor from a row's sort key.
func Encode(key ...string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strings.Join(key, sep)))
}

// Decode reads a cursor of n parts. An empty cursor is the first page (nil, nil).
func Decode(cursor string, n int) ([]string, error) {
	if cursor == "" {
		return nil, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return nil, ErrBadCursor
	}
	parts := strings.Split(string(b), sep)
	if len(parts) != n {
		return nil, ErrBadCursor
	}
	return parts, nil
}

// Make builds a page from rows fetched with limit+1: the extra row only says
// that more follow. key gives a row's sort key (what Encode takes).
func Make[T any](rows []T, limit int, key func(T) []string) Page[T] {
	if rows == nil {
		rows = []T{}
	}
	if len(rows) <= limit {
		return Page[T]{Items: rows}
	}
	rows = rows[:limit]
	return Page[T]{Items: rows, NextCursor: Encode(key(rows[len(rows)-1])...)}
}
