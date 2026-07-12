package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"

	"github.com/go-playground/validator/v10"
)

// validate is shared and thread-safe; struct metadata is cached after
// first use, so construct it exactly once.
var validate = newValidator()

func newValidator() *validator.Validate {
	v := validator.New(validator.WithRequiredStructEnabled())
	// Report field names as their json tag, so clients see "email", not "Email".
	v.RegisterTagNameFunc(func(fld reflect.StructField) string {
		name := strings.SplitN(fld.Tag.Get("json"), ",", 2)[0]
		if name == "-" {
			return ""
		}
		return name
	})
	return v
}

// maxBodyBytes caps request bodies accepted by DecodeValid. JSON API
// payloads are small; anything bigger is a mistake or an attack.
const maxBodyBytes = 1 << 20 // 1 MiB

// DecodeValid decodes the JSON body into T and validates it against its
// `validate` tags. On failure it writes the error response (413 for
// oversized bodies, 400 for broken JSON or unknown fields, 422 with
// per-field messages for validation) and returns ok=false; the caller
// should just return.
func DecodeValid[T any](w http.ResponseWriter, r *http.Request) (T, bool) {
	var req T
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	// Reject fields the DTO doesn't declare, so typos ("emial") fail loudly
	// instead of being silently dropped.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			WriteError(w, http.StatusRequestEntityTooLarge, CodePayloadTooLarge,
				fmt.Sprintf("request body must not exceed %d bytes", maxErr.Limit))
			return req, false
		}
		// Decoder errors describe the client's own payload; safe to echo.
		WriteError(w, http.StatusBadRequest, CodeBadRequest, "invalid request body: "+err.Error())
		return req, false
	}
	if err := validate.Struct(req); err != nil {
		var verrs validator.ValidationErrors
		if !errors.As(err, &verrs) {
			writeValidationError(w, nil)
			return req, false
		}
		fields := make(map[string]string, len(verrs))
		for _, fe := range verrs {
			fields[fe.Field()] = fieldMessage(fe)
		}
		writeValidationError(w, fields)
		return req, false
	}
	return req, true
}

// fieldMessage turns a validator tag into a human-readable message.
// Extend this switch as new tags come into use.
func fieldMessage(fe validator.FieldError) string {
	switch fe.Tag() {
	case "required":
		return "is required"
	case "email":
		return "must be a valid email address"
	case "min":
		return fmt.Sprintf("must be at least %s characters", fe.Param())
	case "max":
		return fmt.Sprintf("must be at most %s characters", fe.Param())
	default:
		return fmt.Sprintf("is invalid (rule: %s)", fe.Tag())
	}
}
