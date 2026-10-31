package project

import (
	"errors"
	"fmt"
)

// Error kinds. Every error a Service method returns for a problem the
// client can act on is an *Error wrapping one of these, so callers can
// branch with errors.Is and show Error.Message to the user.
var (
	ErrNotFound = errors.New("not found")
	ErrInvalid  = errors.New("invalid input") // a field value is not acceptable
	ErrConflict = errors.New("conflict")      // the current state does not allow it
)

// Error is a user-facing error. Message is written in Chinese and is shown
// to users as is.
type Error struct {
	Kind    error
	Message string
}

func (e *Error) Error() string { return e.Message }

func (e *Error) Unwrap() error { return e.Kind }

func newError(kind error, format string, args ...any) *Error {
	return &Error{Kind: kind, Message: fmt.Sprintf(format, args...)}
}

var (
	errProjectNotFound = &Error{Kind: ErrNotFound, Message: "项目不存在"}
	errDatasetNotFound = &Error{Kind: ErrNotFound, Message: "数据集不存在"}
	errNameTaken       = &Error{Kind: ErrConflict, Message: "已存在同名项目"}
)
