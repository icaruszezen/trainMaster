// Package storagenode manages local and remote storage nodes (local
// directories, S3-compatible object stores, and SMB/CIFS shares) for datasets
// and frozen data versions, plus trainer dataset cache policies and online
// cross-node data migrations.
package storagenode

import (
	"errors"
	"fmt"
)

// Error kinds returned by Service and Volume operations.
var (
	ErrNotFound            = errors.New("storage node not found")
	ErrConflict            = errors.New("storage node conflict")
	ErrInvalid             = errors.New("invalid storage node input")
	ErrUnavailable         = errors.New("storage node unavailable")
	ErrInsufficientStorage = errors.New("insufficient storage")
	ErrNotDirectory        = errors.New("path component is not a directory")
)

// Error is a user-facing error with a kind (ErrNotFound, ErrConflict, ...) and
// a Chinese message suitable for HTTP responses.
type Error struct {
	Kind    error
	Message string
	Cause   error
}

func (e *Error) Error() string {
	if e.Cause != nil {
		return e.Message + ": " + e.Cause.Error()
	}
	return e.Message
}

func (e *Error) Unwrap() error {
	if e.Cause != nil {
		return errors.Join(e.Kind, e.Cause)
	}
	return e.Kind
}

func newError(kind error, format string, args ...any) *Error {
	return &Error{Kind: kind, Message: fmt.Sprintf(format, args...)}
}

func wrapError(kind error, cause error, format string, args ...any) *Error {
	return &Error{Kind: kind, Message: fmt.Sprintf(format, args...), Cause: cause}
}
