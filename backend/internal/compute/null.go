package compute

import (
	"encoding/json"
	"math"
	"strconv"
)

// Null is a JSON value that may be null. The node protocol uses null for
// "unknown" and never 0 or "", so every optional number, string and flag
// of a report is a Null.
//
// Decoding never fails: a value of the wrong JSON type, or a number that
// does not fit T, decodes as null. One odd field from a buggy or newer
// node agent must not make its whole heartbeat fail (the node would then
// look offline); the field is simply unknown.
type Null[T any] struct {
	V     T
	Valid bool
}

// Value returns a valid Null holding v.
func Value[T any](v T) Null[T] { return Null[T]{V: v, Valid: true} }

// MarshalJSON encodes V, or null when the value is unknown.
func (n Null[T]) MarshalJSON() ([]byte, error) {
	if !n.Valid {
		return []byte("null"), nil
	}
	return json.Marshal(n.V)
}

// UnmarshalJSON decodes a value of T; anything else becomes null.
func (n *Null[T]) UnmarshalJSON(data []byte) error {
	*n = Null[T]{}
	if string(data) == "null" {
		return nil
	}
	if p, ok := any(&n.V).(*int64); ok {
		// Python agents may send whole numbers as floats (2.0, 1.5e10).
		*p, n.Valid = parseInt(data)
		return nil
	}
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return nil
	}
	n.V, n.Valid = v, true
	return nil
}

// Largest float64 values that convert to int64 without overflow.
const (
	minIntFloat = -(1 << 63)
	maxIntFloat = 1 << 63 // exclusive
)

// parseInt parses a JSON number holding a whole number that fits int64.
func parseInt(data []byte) (int64, bool) {
	s := string(data)
	if v, err := strconv.ParseInt(s, 10, 64); err == nil {
		return v, true
	}
	if len(s) == 0 || (s[0] != '-' && (s[0] < '0' || s[0] > '9')) {
		return 0, false // not a number (ParseFloat would accept "Inf")
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f != math.Trunc(f) || f < minIntFloat || f >= maxIntFloat {
		return 0, false
	}
	return int64(f), true
}
