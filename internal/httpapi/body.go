package httpapi

import (
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
	errTrailingData = errors.New("unexpected data after JSON value")
)

// fieldError says which JSON field broke which rule.
// Rule is a validator tag such as "required", or "type" when the JSON type is wrong.
type fieldError struct {
	Field string
	Rule  string
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

// decodeJSON reads one JSON object of type T from the request body and validates it
// against T's `validate` tags. Unknown fields are rejected.
// Errors: errBodyTooLarge, errInvalidJSON (malformed JSON, unknown field, trailing data)
// or *validationError (wrong JSON type, or a failed rule). An empty body counts as {}.
func decodeJSON[T any](w http.ResponseWriter, r *http.Request, maxBytes int) (T, error) {
	var dst T

	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, int64(maxBytes)))
	dec.DisallowUnknownFields()

	err := dec.Decode(&dst)
	if err == nil {
		err = rejectTrailingData(dec)
	}

	var tooLarge *http.MaxBytesError
	var wrongType *json.UnmarshalTypeError
	switch {
	case err == nil, errors.Is(err, io.EOF):
	case errors.As(err, &tooLarge):
		return dst, errBodyTooLarge
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

// rejectTrailingData fails if anything other than whitespace follows the first JSON value.
func rejectTrailingData(dec *json.Decoder) error {
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errTrailingData
	}

	return nil
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
