package compute

import (
	"bytes"
	"encoding/json"
	"errors"
)

// Decoding of the node agent's request bodies. It is deliberately lenient
// so that newer agents work with older servers and one bad reading does
// not fail a heartbeat:
//   - unknown fields are ignored;
//   - NaN, Infinity and -Infinity, which Python's json module writes for
//     non-finite floats, decode as null;
//   - a value of the wrong JSON type is skipped (Null fields become null,
//     others keep their zero value), as is a report section that is not an
//     object.
//
// Only a body that is not a JSON object is an error.

// registerRequest is the JSON body of a registration. The report sections
// are decoded separately so that one that is not an object reads as
// absent instead of as an empty report.
type registerRequest struct {
	NodeKey      string          `json:"node_key"`
	Name         string          `json:"name"`
	Hostname     string          `json:"hostname"`
	AgentVersion string          `json:"agent_version"`
	Hardware     json.RawMessage `json:"hardware"`
	Environment  json.RawMessage `json:"environment"`
	Metrics      json.RawMessage `json:"metrics"`
}

type heartbeatRequest struct {
	NodeKey     string          `json:"node_key"`
	Hardware    json.RawMessage `json:"hardware"`
	Environment json.RawMessage `json:"environment"`
	Metrics     json.RawMessage `json:"metrics"`
}

// errNotObject rejects a body that is JSON but not an object.
var errNotObject = errors.New("request body is not a JSON object")

// DecodeRegister parses the body of a registration. It does not validate
// the fields; Service.Register does.
func DecodeRegister(body []byte) (RegisterInput, error) {
	var req registerRequest
	if err := decodeObject(body, &req); err != nil {
		return RegisterInput{}, err
	}
	return RegisterInput{
		NodeKey:      req.NodeKey,
		Name:         req.Name,
		Hostname:     req.Hostname,
		AgentVersion: req.AgentVersion,
		Hardware:     decodeSection[Hardware](req.Hardware),
		Environment:  decodeSection[Environment](req.Environment),
		Metrics:      decodeSection[Metrics](req.Metrics),
	}, nil
}

// DecodeHeartbeat parses the body of a heartbeat.
func DecodeHeartbeat(body []byte) (HeartbeatInput, error) {
	var req heartbeatRequest
	if err := decodeObject(body, &req); err != nil {
		return HeartbeatInput{}, err
	}
	return HeartbeatInput{
		NodeKey:     req.NodeKey,
		Hardware:    decodeSection[Hardware](req.Hardware),
		Environment: decodeSection[Environment](req.Environment),
		Metrics:     decodeSection[Metrics](req.Metrics),
	}, nil
}

// decodeObject decodes a request body that must be a JSON object.
func decodeObject(body []byte, v any) error {
	if err := decodeLenient(body, v); err != nil {
		return err
	}
	if b := bytes.TrimLeft(body, " \t\r\n"); len(b) == 0 || b[0] != '{' {
		return errNotObject
	}
	return nil
}

// decodeSection decodes a report section; null, a missing section or one
// that is not an object is nil.
func decodeSection[T any](raw json.RawMessage) *T {
	if len(raw) == 0 || raw[0] != '{' {
		return nil
	}
	var v T
	// raw is part of a document that already decoded, so it is valid.
	if decodeLenient(raw, &v) != nil {
		return nil
	}
	return &v
}

// decodeLenient is json.Unmarshal with the leniency described above.
func decodeLenient(data []byte, v any) error {
	err := json.Unmarshal(nonFiniteToNull(data), v)
	// encoding/json skips a value of the wrong type, decodes the rest of
	// the document and then reports the first skipped value.
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		return nil
	}
	return err
}

var nonFiniteTokens = [][]byte{[]byte("NaN"), []byte("Infinity"), []byte("-Infinity")}

// nonFiniteToNull replaces the tokens NaN, Infinity and -Infinity outside
// of strings by null. It returns data itself when there are none.
func nonFiniteToNull(data []byte) []byte {
	if !bytes.Contains(data, []byte("NaN")) && !bytes.Contains(data, []byte("Infinity")) {
		return data
	}
	var out []byte
	last := 0 // data[last:i] is not copied to out yet
	inString, escaped := false, false
	for i := 0; i < len(data); i++ {
		c := data[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			continue
		}
		for _, tok := range nonFiniteTokens {
			if bytes.HasPrefix(data[i:], tok) && tokenEnds(data, i+len(tok)) {
				out = append(out, data[last:i]...)
				out = append(out, "null"...)
				i += len(tok) - 1
				last = i + 1
				break
			}
		}
	}
	if out == nil {
		return data
	}
	return append(out, data[last:]...)
}

// tokenEnds reports whether a bare JSON token may end before data[i].
func tokenEnds(data []byte, i int) bool {
	if i == len(data) {
		return true
	}
	switch data[i] {
	case ',', ']', '}', ' ', '\t', '\n', '\r':
		return true
	}
	return false
}
