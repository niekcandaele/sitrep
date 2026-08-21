package model

import (
	"encoding/json"
	"fmt"
)

// enumToken pairs one enum value with the single token that represents it in
// both the wire format and display. Defining the pair once is what keeps
// String and the JSON form from drifting apart.
type enumToken[T comparable] struct {
	value T
	token string
}

// enumTokens is an ordered token table for one enum type.
type enumTokens[T comparable] []enumToken[T]

// token returns the token for v, or a diagnostic placeholder for a value
// outside the table.
func (e enumTokens[T]) token(v T, kind string) string {
	for _, t := range e {
		if t.value == v {
			return t.token
		}
	}
	return fmt.Sprintf("%s(%v)", kind, v)
}

// value returns the enum value for a token.
func (e enumTokens[T]) value(token string) (T, bool) {
	for _, t := range e {
		if t.token == token {
			return t.value, true
		}
	}
	var zero T
	return zero, false
}

// has reports whether v is one of the table's values.
func (e enumTokens[T]) has(v T) bool {
	for _, t := range e {
		if t.value == v {
			return true
		}
	}
	return false
}

// marshal encodes v as its token.
func (e enumTokens[T]) marshal(v T) ([]byte, error) {
	for _, t := range e {
		if t.value == v {
			return json.Marshal(t.token)
		}
	}
	return nil, fmt.Errorf("cannot marshal out-of-range value %v", v)
}

// unmarshal decodes a token into out.
func (e enumTokens[T]) unmarshal(b []byte, out *T, kind string) error {
	var token string
	if err := json.Unmarshal(b, &token); err != nil {
		return fmt.Errorf("%s must be a JSON string: %w", kind, err)
	}
	v, ok := e.value(token)
	if !ok {
		return fmt.Errorf("unknown %s %q", kind, token)
	}
	*out = v
	return nil
}
