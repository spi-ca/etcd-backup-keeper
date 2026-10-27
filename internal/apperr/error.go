// Package apperr defines the only errors safe to display to a CLI caller.
package apperr

import (
	"context"
	"errors"
)

// Error deliberately has no underlying error: parser, filesystem, TLS and RPC
// diagnostics can contain credentials, configuration values or private paths.
// Messages must be static, operator-facing text, never raw external errors.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }

func New(code, message string) *Error { return &Error{Code: code, Message: message} }

// Safe is the output boundary. Unknown errors are never printed verbatim.
func Safe(err error) *Error {
	if errors.Is(err, context.Canceled) {
		return New("canceled", "operation canceled")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return New("timeout", "operation timed out")
	}
	var safe *Error
	if errors.As(err, &safe) {
		return safe
	}
	return New("internal_error", "operation failed")
}
