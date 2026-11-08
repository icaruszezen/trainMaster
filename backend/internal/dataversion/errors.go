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

// Messages of the build and of the checks before it.
const (
	// msgInternal: a build failed for a reason users cannot act on (logged).
	msgInternal = "生成失败（服务器内部错误）"
	// msgInterrupted: the server stopped while the version was building.
	msgInterrupted = "服务重启，生成被中断，可以重新生成"
)

var (
	errNoClasses      = &Error{Kind: ErrConflict, Message: "项目还没有类别，请先在项目中添加类别"}
	errClassesChanged = &Error{Kind: ErrConflict, Message: "项目的类别列表已经修改，请检查后重新生成"}
	errNoImages       = &Error{Kind: ErrConflict, Message: "没有可以放进数据版本的图片，请检查所选数据集的标注、类别列表和过滤设置"}
	errNoValImages    = &Error{Kind: ErrConflict, Message: "划分后验证集没有图片，请调整划分比例、换一个随机种子，或增加图片"}
	errNoTrainImages  = &Error{Kind: ErrConflict, Message: "划分后训练集没有图片，请调整划分比例、换一个随机种子，或增加图片"}
	errNotFailed      = &Error{Kind: ErrConflict, Message: "只有生成失败的数据版本可以重新生成"}
	// errDatasetGone: a source of a create or preview body that does not
	// exist or is in the recycle bin.
	errDatasetGone = &Error{Kind: ErrInvalid, Message: "数据集不存在（可能已被删除）"}
	// errReviewVersionGone: a selected review version of a create or
	// preview body that does not exist, belongs to another dataset or is
	// in the recycle bin.
	errReviewVersionGone = &Error{Kind: ErrInvalid, Message: "所选审图版本不存在（可能已被删除）"}
)
