package evaluation

import (
	"errors"
	"fmt"
)

// Error kinds returned by evaluation.Service so HTTP handlers map them cleanly
// to status codes while showing Error.Message directly to users.
var (
	ErrNotFound   = errors.New("evaluation not found")
	ErrInvalid    = errors.New("invalid evaluation input")
	ErrBadRequest = errors.New("bad evaluation request")
	ErrConflict   = errors.New("evaluation conflict")
)

// Error is a user-facing error with a Chinese message.
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
	errEvalNotFound   = &Error{Kind: ErrNotFound, Message: "评估任务不存在"}
	errSampleNotFound = &Error{Kind: ErrNotFound, Message: "评估样本不存在"}
	errImageMissing   = &Error{Kind: ErrNotFound, Message: "源图片已不在磁盘上（原数据集或数据版本可能已被删除）"}
	errTaskNotYours   = &Error{Kind: ErrConflict, Message: "该评估任务已不属于当前训练节点（可能已被停止或租约已过期）"}
	errNotSucceeded   = &Error{Kind: ErrConflict, Message: "评估任务尚未完成，暂无评估结果"}
)
