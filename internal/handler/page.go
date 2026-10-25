package handler

import (
	"fmt"
	"net/http"
	"strconv"
)

// Pagination bounds applied to every list endpoint.
const (
	// DefaultLimit is used when ?limit is absent.
	DefaultLimit int32 = 20
	// MaxLimit caps ?limit so a client cannot ask for an unbounded scan.
	MaxLimit int32 = 100
)

// Page holds validated list pagination parameters.
type Page struct {
	Limit  int32
	Offset int32
}

// Meta returns the pagination echo for a list response. Page and Meta share a
// shape today, but Meta is the API contract and may gain fields Page must not.
func (p Page) Meta() Meta { return Meta(p) }

// DecodePage reads ?limit and ?offset. An absent parameter takes its default;
// a malformed or out-of-range one is rejected with 422 and returns ok=false.
// Rejecting beats clamping: silently substituting a default hides the error.
func DecodePage(w http.ResponseWriter, r *http.Request) (Page, bool) {
	q := r.URL.Query()
	fields := map[string]string{}

	limit := DefaultLimit
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 32)
		switch {
		case err != nil:
			fields["limit"] = "must be an integer"
		case n < 1:
			fields["limit"] = "must be at least 1"
		case int32(n) > MaxLimit:
			fields["limit"] = fmt.Sprintf("must be at most %d", MaxLimit)
		default:
			limit = int32(n)
		}
	}

	offset := int32(0)
	if raw := q.Get("offset"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 32)
		switch {
		case err != nil:
			fields["offset"] = "must be an integer"
		case n < 0:
			fields["offset"] = "must not be negative"
		default:
			offset = int32(n)
		}
	}

	if len(fields) > 0 {
		writeValidationError(w, fields)
		return Page{}, false
	}
	return Page{Limit: limit, Offset: offset}, true
}
