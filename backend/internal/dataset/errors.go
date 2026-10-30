package dataset

import (
	"errors"
	"fmt"
)

// Error kinds. Every error a Service method returns for a problem the
// client can act on is an *Error wrapping one of these, so callers can
// branch with errors.Is and show Error.Message to the user.
var (
	ErrNotFound            = errors.New("not found")
	ErrInvalid             = errors.New("invalid input")               // a field value is not acceptable
	ErrBadRequest          = errors.New("malformed request")           // the request itself is malformed
	ErrConflict            = errors.New("conflict")                    // the current state does not allow it
	ErrOffsetMismatch      = errors.New("upload offset mismatch")      // archive chunk at the wrong offset
	ErrTooLarge            = errors.New("payload too large")           // request body over the limit
	ErrInsufficientStorage = errors.New("insufficient storage")        // not enough free disk space
	ErrUnsupportedImage    = errors.New("unsupported image")           // no thumbnail can be generated
	errCanceled            = errors.New("import canceled by the user") // cancel cause of a running job
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
	errDatasetNotFound = &Error{Kind: ErrNotFound, Message: "数据集不存在"}
	errImageNotFound   = &Error{Kind: ErrNotFound, Message: "图片不存在"}
	errImportNotFound  = &Error{Kind: ErrNotFound, Message: "导入任务不存在"}
)
