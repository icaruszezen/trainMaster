package training

import (
	"errors"
	"fmt"
)

// Error kinds returned by Service methods. Callers branch with errors.Is and
// show Error.Message directly to users.
var (
	ErrNotFound = errors.New("not found")
	ErrInvalid  = errors.New("invalid input")
	ErrConflict = errors.New("conflict")
)

// Error is a user-facing error whose Message is written in Simplified Chinese.
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
	errJobNotFound         = &Error{Kind: ErrNotFound, Message: "训练任务不存在"}
	errProjectNotFound     = &Error{Kind: ErrNotFound, Message: "项目不存在"}
	errDataVersionNotFound = &Error{Kind: ErrNotFound, Message: "数据版本不存在"}
	errBaseModelNotFound   = &Error{Kind: ErrNotFound, Message: "基础模型不存在"}
	errArtifactNotFound    = &Error{Kind: ErrNotFound, Message: "训练产物文件不存在"}
	errTaskGone            = &Error{Kind: ErrNotFound, Message: "训练任务已不存在"}
	errTaskNotYours        = &Error{Kind: ErrConflict, Message: "该训练任务未被此节点持有（可能已取消、超时重入队或已结束）"}
)
