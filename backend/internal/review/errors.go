package review

import (
	"errors"
	"fmt"
)

// Error kinds. Every error a Service method returns for a problem the
// client can act on is an *Error wrapping one of these, so callers can
// branch with errors.Is and show Error.Message to the user. Errors of the
// dataset images themselves (a missing file, a thumbnail that cannot be
// made) are *dataset.Error values, passed through as they are.
var (
	ErrNotFound            = errors.New("not found")
	ErrInvalid             = errors.New("invalid input")      // a field value is not acceptable
	ErrBadRequest          = errors.New("malformed request")  // the request itself is malformed
	ErrConflict            = errors.New("conflict")           // the current state does not allow it
	ErrAnnotationChanged   = errors.New("annotation changed") // the VOC file is not the version an edit started from
	ErrTooLarge            = errors.New("payload too large")  // request body over the limit
	ErrInsufficientStorage = errors.New("insufficient storage")
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
	errReviewNotFound = &Error{Kind: ErrNotFound, Message: "审图任务不存在"}
	errImageNotFound  = &Error{Kind: ErrNotFound, Message: "图片不存在（可能已随数据集或重新扫描被移除）"}
	errNotInferred    = &Error{Kind: ErrConflict, Message: "这张图片还没有识别完成"}
	errMergeRunning   = &Error{Kind: ErrConflict, Message: "正在合并到数据集，请等合并完成后再删除"}
	// errAnnotationChanged: the review VOC file changed since the version an
	// edit started from.
	errAnnotationChanged = &Error{Kind: ErrAnnotationChanged,
		Message: "这张图片的审图标注已被修改（可能有其他人同时在审这张图），请重新加载后再编辑，或选择覆盖"}

	// Agent task calls for a review that was deleted meanwhile (the node
	// stops working on it) or that another node holds / that has ended.
	errTaskGone     = &Error{Kind: ErrNotFound, Message: "审图任务不存在（可能已被删除）"}
	errTaskNotYours = &Error{Kind: ErrConflict, Message: "审图任务的识别已不属于该节点或已结束"}
)

// Messages of reviews that cannot be inferred.
const (
	msgModelDeleted     = "模型已被删除，无法识别"
	msgModelFileMissing = "模型文件不存在，无法识别"
)
