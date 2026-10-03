// Package errs defines the error taxonomy used across Talon.
//
// Every error surfaced to the user carries a machine-readable Kind, a short
// human message and, when useful, a Hint describing how to recover. Callers
// use Wrap/Wrapf to attach context; the CLI prints Error.User() instead of a
// stack trace.
package errs

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Kind classifies a failure. It drives retry logic, exit codes and wording.
type Kind string

const (
	KindConfig     Kind = "config"
	KindAuth       Kind = "auth"
	KindNetwork    Kind = "network"
	KindTimeout    Kind = "timeout"
	KindRateLimit  Kind = "rate-limit"
	KindNotFound   Kind = "not-found"
	KindPermission Kind = "permission"
	KindExecution  Kind = "execution"
	KindModel      Kind = "model"
	KindParse      Kind = "parse"
	KindCancelled  Kind = "cancelled"
	KindUsage      Kind = "usage"
	KindInternal   Kind = "internal"
)

// Error is the concrete error type used everywhere in Talon.
type Error struct {
	Kind Kind
	Op   string
	Msg  string
	Hint string
	Err  error
}

func (e *Error) Error() string {
	var b strings.Builder
	if e.Op != "" {
		b.WriteString(e.Op)
		b.WriteString(": ")
	}
	b.WriteString(e.Msg)
	if e.Err != nil {
		b.WriteString(": ")
		b.WriteString(e.Err.Error())
	}
	return b.String()
}

func (e *Error) Unwrap() error { return e.Err }

// User renders the error the way it should be shown to a human.
func (e *Error) User() string {
	s := e.Msg
	if e.Err != nil && e.Msg != "" {
		s = e.Msg + " (" + e.Err.Error() + ")"
	} else if e.Err != nil {
		s = e.Err.Error()
	}
	if e.Hint != "" {
		s += "\n  hint: " + e.Hint
	}
	return s
}

// New builds an error of the given kind.
func New(kind Kind, op, msg string) *Error {
	return &Error{Kind: kind, Op: op, Msg: msg}
}

// Newf is New with formatting.
func Newf(kind Kind, op, format string, args ...any) *Error {
	return &Error{Kind: kind, Op: op, Msg: fmt.Sprintf(format, args...)}
}

// Wrap attaches operation context to an existing error. If err is nil it
// returns nil, so it is safe to use inline on returned values.
func Wrap(kind Kind, op string, err error) error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) && e.Op == "" {
		e.Op = op
		return e
	}
	return &Error{Kind: kind, Op: op, Msg: msgFrom(err), Err: err}
}

func msgFrom(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Msg
	}
	return ""
}

// WrapHint attaches a recovery hint to an error.
func WrapHint(kind Kind, op, hint string, err error) error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		if e.Hint == "" {
			e.Hint = hint
		}
		return e
	}
	return &Error{Kind: kind, Op: op, Msg: err.Error(), Err: err, Hint: hint}
}

// Shorthand constructors for the kinds used most often.

func Config(op, format string, args ...any) *Error {
	return Newf(KindConfig, op, format, args...)
}
func Usage(format string, args ...any) *Error {
	return Newf(KindUsage, "", format, args...)
}
func NotFound(op, format string, args ...any) *Error {
	return Newf(KindNotFound, op, format, args...)
}
func Permission(op, format string, args ...any) *Error {
	return Newf(KindPermission, op, format, args...)
}
func Network(op, format string, args ...any) *Error {
	return Newf(KindNetwork, op, format, args...)
}
func Internal(op, format string, args ...any) *Error {
	return Newf(KindInternal, op, format, args...)
}
func Parse(op, format string, args ...any) *Error {
	return Newf(KindParse, op, format, args...)
}
func Cancelled(op, format string, args ...any) *Error {
	return Newf(KindCancelled, op, format, args...)
}

// KindOf reports the kind of err, defaulting to KindInternal.
func KindOf(err error) Kind {
	if err == nil {
		return ""
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	if errors.Is(err, context.Canceled) {
		return KindCancelled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return KindTimeout
	}
	return KindInternal
}

// IsKind reports whether err has the given kind.
func IsKind(err error, kind Kind) bool { return KindOf(err) == kind }

// User renders any error for terminal display.
func User(err error) string {
	if err == nil {
		return ""
	}
	var e *Error
	if errors.As(err, &e) {
		return e.User()
	}
	if errors.Is(err, context.Canceled) {
		return "operation cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "operation timed out"
	}
	return err.Error()
}

// Retryable reports whether retrying the operation could plausibly succeed.
func Retryable(err error) bool {
	switch KindOf(err) {
	case KindNetwork, KindTimeout, KindRateLimit:
		return true
	default:
		return false
	}
}

// Hint returns the hint attached to err, if any.
func Hint(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Hint
	}
	return ""
}
