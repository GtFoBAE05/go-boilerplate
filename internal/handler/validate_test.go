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

	got, ok := DecodeValid[pointerDTO](rec, r)
	if !ok {
		t.Fatalf("decode failed: %d %s", rec.Code, rec.Body.String())
	}
	if got.Name != "ada" {
		t.Fatalf("want normalized name, got %q", got.Name)
	}
}
