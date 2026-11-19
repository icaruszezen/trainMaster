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
	errJobNotFound             = &Error{Kind: ErrNotFound, Message: "训练任务不存在"}
	errProjectNotFound         = &Error{Kind: ErrNotFound, Message: "项目不存在"}
	errDataVersionNotFound     = &Error{Kind: ErrNotFound, Message: "数据版本不存在"}
	errBaseModelNotFound       = &Error{Kind: ErrNotFound, Message: "基础模型不存在"}
	errTemplateNotFound        = &Error{Kind: ErrNotFound, Message: "参数模板不存在"}
	errTemplateNameExists      = &Error{Kind: ErrConflict, Message: "已存在同名的参数模板，请更换名称"}
	errTemplateBuiltinReadOnly = &Error{Kind: ErrConflict, Message: "系统内置模板为只读预设，不支持直接修改名称、说明或参数，请复制为自定义模板后编辑"}
	errTemplateBuiltinDelete   = &Error{Kind: ErrConflict, Message: "系统内置模板不可删除"}
	errArtifactNotFound        = &Error{Kind: ErrNotFound, Message: "训练产物文件不存在"}
	errTaskGone                = &Error{Kind: ErrNotFound, Message: "训练任务已不存在"}
	errTaskNotYours            = &Error{Kind: ErrConflict, Message: "该训练任务未被此节点持有（可能已取消、超时重入队或已结束）"}
)
