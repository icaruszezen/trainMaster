package dataversion

import (
	"errors"
	"fmt"
)

// Error kinds. Every error a Service method returns for a problem the
// client can act on is an *Error wrapping one of these, so callers can
// branch with errors.Is and show Error.Message to the user.
var (
	ErrNotFound            = errors.New("not found")
	ErrInvalid             = errors.New("invalid input")        // a field value is not acceptable
	ErrBadRequest          = errors.New("malformed request")    // the request itself is malformed
	ErrConflict            = errors.New("conflict")             // the current state does not allow it
	ErrNameTaken           = errors.New("name taken")           // restoring: the project's name is taken
	ErrInsufficientStorage = errors.New("insufficient storage") // not enough free disk space
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
	errVersionNotFound = &Error{Kind: ErrNotFound, Message: "数据版本不存在（可能已被删除）"}
	errImageNotFound   = &Error{Kind: ErrNotFound, Message: "图片不存在"}
	// errProjectNotFound: the project of a create or preview body (a
	// field, hence 422, not 404).
	errProjectNotFound = &Error{Kind: ErrInvalid, Message: "项目不存在（可能已被删除）"}
	// errNotReady: the operation needs a ready version.
	errNotReady = &Error{Kind: ErrConflict, Message: "数据版本还没有生成完成"}
)
