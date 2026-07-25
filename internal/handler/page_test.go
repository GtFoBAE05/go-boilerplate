package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDecodePageDefaults(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/users", nil)

	page, ok := DecodePage(rec, r)
	if !ok {
		t.Fatalf("want ok for absent params, got body %s", rec.Body.String())
	}
	if page.Limit != DefaultLimit || page.Offset != 0 {
		t.Fatalf("want limit=%d offset=0, got %+v", DefaultLimit, page)
	}
}

func TestDecodePageAcceptsValidValues(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/users?limit=50&offset=100", nil)

	page, ok := DecodePage(rec, r)
	if !ok {
		t.Fatalf("want ok, got body %s", rec.Body.String())
	}
	if page.Limit != 50 || page.Offset != 100 {
		t.Fatalf("want limit=50 offset=100, got %+v", page)
	}
}

// Rejecting rather than clamping: a silently substituted page is a page the
// client never asked for and cannot detect.
func TestDecodePageRejectsBadValues(t *testing.T) {
	tests := []struct {
		query     string
		wantField string
	}{
		{"limit=abc", "limit"},
		{"limit=0", "limit"},
		{"limit=-3", "limit"},
		{fmt.Sprintf("limit=%d", MaxLimit+1), "limit"},
		{"limit=99999999999999999999", "limit"},
		{"offset=abc", "offset"},
		{"offset=-1", "offset"},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			rec := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, "/users?"+tt.query, nil)

			if _, ok := DecodePage(rec, r); ok {
				t.Fatalf("want rejection for %q", tt.query)
			}
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("want 422, got %d", rec.Code)
			}
			got := decodeError(t, rec.Body.Bytes())
			if got.Code != CodeValidationFailed {
				t.Fatalf("want %q, got %q", CodeValidationFailed, got.Code)
			}
			if _, ok := got.Fields[tt.wantField]; !ok {
				t.Fatalf("want a field error for %q, got %v", tt.wantField, got.Fields)
			}
		})
	}
}

// Both parameters are reported in one response, so a client fixes its request
// once instead of discovering errors one round trip at a time.
func TestDecodePageReportsBothFields(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/users?limit=abc&offset=-1", nil)

	if _, ok := DecodePage(rec, r); ok {
		t.Fatal("want rejection")
	}
	got := decodeError(t, rec.Body.Bytes())
	if len(got.Fields) != 2 {
		t.Fatalf("want both limit and offset reported, got %v", got.Fields)
	}
}
