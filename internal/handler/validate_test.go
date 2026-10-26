package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type pointerDTO struct {
	Name string `json:"name" validate:"required,min=1"`
}

func (d *pointerDTO) Normalize() { d.Name = strings.TrimSpace(d.Name) }

func TestDecodeValidNormalizesPointerDTO(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"  ada  "}`))
	r.Header.Set("Content-Type", "application/json")

	got, ok := DecodeValid[*pointerDTO](rec, r)
	if !ok {
		t.Fatalf("decode failed: %d %s", rec.Code, rec.Body.String())
	}
	if got.Name != "ada" {
		t.Fatalf("want normalized name, got %q", got.Name)
	}
}

func TestDecodeValidNormalizesValueDTO(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"  ada  "}`))
	r.Header.Set("Content-Type", "application/json")

	got, ok := DecodeValid[pointerDTO](rec, r)
	if !ok {
		t.Fatalf("decode failed: %d %s", rec.Code, rec.Body.String())
	}
	if got.Name != "ada" {
		t.Fatalf("want normalized name, got %q", got.Name)
	}
}

func TestDecodeValidRequiresJSONContentType(t *testing.T) {
	for _, tt := range []struct {
		name        string
		contentType string
		want        int
	}{
		{"missing", "", http.StatusUnsupportedMediaType},
		{"form", "application/x-www-form-urlencoded", http.StatusUnsupportedMediaType},
		{"json with charset", "application/json; charset=utf-8", http.StatusOK},
		{"structured json", "application/vnd.api+json", http.StatusOK},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"ada"}`))
			if tt.contentType != "" {
				r.Header.Set("Content-Type", tt.contentType)
			}
			if _, ok := DecodeValid[pointerDTO](rec, r); !ok {
				if rec.Code != tt.want {
					t.Fatalf("want %d, got %d (body: %s)", tt.want, rec.Code, rec.Body.String())
				}
				return
			}
			if tt.want != http.StatusOK {
				t.Fatalf("want %d, got success", tt.want)
			}
		})
	}
}
