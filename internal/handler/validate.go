package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
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

// maxBodyBytes caps DecodeValid bodies; JSON API payloads are small.
const maxBodyBytes = 1 << 20 // 1 MiB

// isJSONContentType accepts application/json and structured +json media types,
// with optional parameters such as charset.
func isJSONContentType(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return false
	}
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}

// normalizer canonicalizes before validation, keeping decode -> normalize ->
// validate identical for every resource. Doing it after validation lets
// whitespace-only input pass `required,min=1` and reach the database empty.
type normalizer interface{ Normalize() }

// DecodeValid decodes and validates the JSON body, writing the error response
// (415, 413, 400, or 422) and returning ok=false on failure.
func DecodeValid[T any](w http.ResponseWriter, r *http.Request) (T, bool) {
	var req T
	if !isJSONContentType(r.Header.Get("Content-Type")) {
		WriteError(w, http.StatusUnsupportedMediaType, CodeUnsupportedMediaType,
			"Content-Type must be application/json")
		return req, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			WriteError(w, http.StatusRequestEntityTooLarge, CodePayloadTooLarge,
				fmt.Sprintf("request body must not exceed %d bytes", maxErr.Limit))
			return req, false
		}
		WriteError(w, http.StatusBadRequest, CodeBadRequest, "invalid request body: "+err.Error())
		return req, false
	}
	// Go's decoder keeps the last duplicate key, which can disagree with what a
	// proxy or audit log saw. Reject duplicates before decoding.
	if err := rejectDuplicateKeys(body); err != nil {
		WriteError(w, http.StatusBadRequest, CodeBadRequest, "invalid request body: "+err.Error())
		return req, false
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	// Reject fields the DTO doesn't declare, so typos ("emial") fail loudly
	// instead of being silently dropped.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		// Decoder errors describe the client's own payload; safe to echo.
		WriteError(w, http.StatusBadRequest, CodeBadRequest, "invalid request body: "+err.Error())
		return req, false
	}
	// One JSON value per request. Without this, a second object is decoded by
	// nobody and ignored, which makes a client-side duplicate or a smuggling
	// attempt look like success.
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		WriteError(w, http.StatusBadRequest, CodeBadRequest, "invalid request body: unexpected data after the JSON object")
		return req, false
	}
	// Normalize before validating: validation must see the value that will be
	// persisted, not the one the client happened to send.
	if n, ok := any(&req).(normalizer); ok {
		n.Normalize()
	} else if n, ok := any(req).(normalizer); ok {
		// T may already be a pointer type; then &req is **T and matches nothing.
		n.Normalize()
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

// rejectDuplicateKeys fails on a repeated key in any object before DTO decoding,
// so it is independent of the struct shape and covers nested objects.
func rejectDuplicateKeys(body []byte) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := scanJSONValue(dec); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("unexpected data after the JSON value")
	}
	return nil
}

func scanJSONValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]struct{}{}
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := keyTok.(string)
			if !ok {
				return errors.New("object key is not a string")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate object key %q", key)
			}
			seen[key] = struct{}{}
			if err := scanJSONValue(dec); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil {
			return err
		}
		if end != json.Delim('}') {
			return errors.New("unterminated object")
		}
	case '[':
		for dec.More() {
			if err := scanJSONValue(dec); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil {
			return err
		}
		if end != json.Delim(']') {
			return errors.New("unterminated array")
		}
	}
	return nil
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
