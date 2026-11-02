package compute

import (
	"errors"
	"fmt"
)

// Error kinds. Every error a Service method returns for a problem the
// client can act on is an *Error wrapping one of these, so callers can
// branch with errors.Is and show Error.Message to the user.
var (
	ErrNotFound    = errors.New("not found")
	ErrInvalid     = errors.New("invalid input") // a field value is not acceptable
	ErrConflict    = errors.New("conflict")      // the current state does not allow it
	ErrUnknownNode = errors.New("unknown node")  // a heartbeat for a node_key that is not registered
)

// Error is a user-facing error. Message is written in Chinese and is shown
// to users (or logged by the node agent) as is.
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
	errNodeNotFound = &Error{Kind: ErrNotFound, Message: "节点不存在"}
	errNodeOnline   = &Error{Kind: ErrConflict, Message: "节点在线，先停止该节点上的 trainer 服务再删除"}
	errNodeOffline  = &Error{Kind: ErrConflict, Message: "节点离线，无法重新检测"}
	// The node agent answers this by registering again.
	errUnknownNode     = &Error{Kind: ErrUnknownNode, Message: "节点未注册或已被删除，请重新注册"}
	errHardwareMissing = &Error{Kind: ErrInvalid, Message: "缺少硬件信息 hardware"}
)
