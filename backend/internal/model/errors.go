package model

import (
	"errors"
	"fmt"
)

// Error kinds. Every error a Service method returns for a problem the
// client can act on is an *Error wrapping one of these, so callers can
// branch with errors.Is and show Error.Message to the user.
var (
	ErrNotFound            = errors.New("not found")
	ErrInvalid             = errors.New("invalid input")          // a field value is not acceptable
	ErrBadRequest          = errors.New("malformed request")      // the request itself is malformed
	ErrConflict            = errors.New("conflict")               // the current state does not allow it
	ErrOffsetMismatch      = errors.New("upload offset mismatch") // upload chunk at the wrong offset
	ErrTooLarge            = errors.New("payload too large")      // request body over the limit
	ErrInsufficientStorage = errors.New("insufficient storage")   // not enough free disk space
	ErrInvalidModel        = errors.New("invalid model")          // the file is not a YOLO model the platform supports
	ErrVersionMismatch     = errors.New("yolo version mismatch")  // the file's YOLO version differs from the chosen one
)

// Error is a user-facing error. Message is written in Chinese and is shown
// to users as is.
type Error struct {
	Kind    error
	Message string
	// detected is the YOLO version found in the file (ErrVersionMismatch).
	detected string
}

func (e *Error) Error() string { return e.Message }

func (e *Error) Unwrap() error { return e.Kind }

func newError(kind error, format string, args ...any) *Error {
	return &Error{Kind: kind, Message: fmt.Sprintf(format, args...)}
}

var (
	errModelNotFound   = &Error{Kind: ErrNotFound, Message: "模型不存在"}
	errUploadNotFound  = &Error{Kind: ErrNotFound, Message: "上传任务不存在"}
	errExportNotFound  = &Error{Kind: ErrNotFound, Message: "转换任务不存在"}
	errProjectNotFound = &Error{Kind: ErrNotFound, Message: "项目不存在"}
	// A project chosen for a new model does not exist (any more).
	errProjectGone = &Error{Kind: ErrInvalid, Message: "所选项目不存在或已被删除，请刷新后重试"}
	errFileMissing = &Error{Kind: ErrNotFound, Message: "模型文件不存在，可能已在服务器上被删除或移动"}
	// Agent task calls for an export that was deleted meanwhile (the node
	// stops working on it) or that another node holds / that has ended.
	errTaskGone     = &Error{Kind: ErrNotFound, Message: "转换任务不存在（可能已被删除）"}
	errTaskNotYours = &Error{Kind: ErrConflict, Message: "转换任务已不属于该节点或已结束"}
)
