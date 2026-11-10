package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"

	"github.com/go-playground/validator/v10"
)

var (
	errBodyTooLarge = errors.New("request body too large")
	errInvalidJSON  = errors.New("invalid JSON body")
	errNotObject    = errors.New("body is not a JSON object")
)

// fieldError says which JSON field broke which rule.
// Rule is a validator tag such as "required", or "type" when the JSON type is wrong.
type fieldError struct {
	Field string `json:"field"`
	Rule  string `json:"rule"`
}

// validationError is returned when the body is valid JSON but does not match the request type.
type validationError struct {
	Fields []fieldError
}

func (e *validationError) Error() string {
	parts := make([]string, len(e.Fields))
	for i, f := range e.Fields {
		parts[i] = f.Field + ": " + f.Rule
	}

	return "validation failed: " + strings.Join(parts, ", ")
}

// validate is safe for concurrent use and caches struct metadata, so it is shared.
var validate = newValidate()

func newValidate() *validator.Validate {
	v := validator.New(validator.WithRequiredStructEnabled())
	// notblank: a string with at least one non-whitespace character.
	// It is registered here because this validator version has no such tag.
	v.RegisterValidation("notblank", func(fl validator.FieldLevel) bool { //nolint:errcheck,gosec // fails only for an empty tag name or nil function.
		return strings.TrimSpace(fl.Field().String()) != ""
	})
	v.RegisterTagNameFunc(func(f reflect.StructField) string {
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" {
			return ""
		}

		return name
	})

	return v
}

// readBody reads the whole request body, failing with errBodyTooLarge past maxBytes.
func readBody(w http.ResponseWriter, r *http.Request, maxBytes int) ([]byte, error) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, int64(maxBytes)))
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return nil, errBodyTooLarge
	}
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	return raw, nil
}

// decodeJSON decodes one JSON object of type T from raw and validates it
// against T's `validate` tags. Unknown fields are rejected.
// An empty body counts as {}, so it fails the "required" rules.
// Errors: errNotObject (anything but exactly one JSON object: plain text, trailing data, a scalar, an array),
// errInvalidJSON (an unknown field) or *validationError (wrong JSON type, or a failed rule).
func decodeJSON[T any](raw []byte) (T, error) {
	var dst T

	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	if !json.Valid(raw) || raw[0] != '{' {
		return dst, errNotObject
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()

	var wrongType *json.UnmarshalTypeError
	switch err := dec.Decode(&dst); {
	case err == nil:
	case errors.As(err, &wrongType) && wrongType.Field != "":
		return dst, &validationError{Fields: []fieldError{{Field: wrongType.Field, Rule: "type"}}}
	default:
		return dst, fmt.Errorf("%w: %w", errInvalidJSON, err)
	}

	if err := validate.Struct(dst); err != nil {
		return dst, toValidationError(err)
	}

	return dst, nil
}

func toValidationError(err error) error {
	var invalid validator.ValidationErrors
	if !errors.As(err, &invalid) {
		return fmt.Errorf("validate: %w", err)
	}

	fields := make([]fieldError, len(invalid))
	for i, fe := range invalid {
		_, path, _ := strings.Cut(fe.Namespace(), ".") // drop the root struct name
		fields[i] = fieldError{Field: path, Rule: fe.Tag()}
	}

	return &validationError{Fields: fields}
}
