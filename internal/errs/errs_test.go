package errs

import (
	"context"
	"errors"
	"testing"
)

func TestKindAndWrapping(t *testing.T) {
	base := errors.New("connection refused")
	wrapped := Wrap(KindNetwork, "llm", base)
	if KindOf(wrapped) != KindNetwork {
		t.Errorf("kind = %v", KindOf(wrapped))
	}
	if !errors.Is(wrapped, base) {
		t.Error("the cause must stay unwrappable")
	}
	if Wrap(KindNetwork, "llm", nil) != nil {
		t.Error("wrapping nil must return nil")
	}
	// A second Wrap keeps the operation but must not clobber the kind.
	twice := Wrap(KindNetwork, "agent", wrapped)
	if KindOf(twice) != KindNetwork {
		t.Errorf("kind after two wraps = %v", KindOf(twice))
	}
}

func TestPlainErrorsAreInternal(t *testing.T) {
	if KindOf(errors.New("boom")) != KindInternal {
		t.Error("a plain error should be internal")
	}
	if KindOf(context.Canceled) != KindCancelled {
		t.Error("context.Canceled should map to cancelled")
	}
	if KindOf(context.DeadlineExceeded) != KindTimeout {
		t.Error("context.DeadlineExceeded should map to timeout")
	}
	if KindOf(nil) != "" {
		t.Error("nil has no kind")
	}
}

func TestUserRenderingIncludesHint(t *testing.T) {
	e := New(KindAuth, "llm", "no API key")
	e.Hint = "export AI_API_KEY"
	got := User(e)
	if got != "no API key\n  hint: export AI_API_KEY" {
		t.Errorf("User = %q", got)
	}
	withoutHint := New(KindAuth, "llm", "no API key")
	if User(withoutHint) != "no API key" {
		t.Errorf("User = %q", User(withoutHint))
	}
	if User(nil) != "" {
		t.Error("nil should render as an empty string")
	}
	if User(errors.New("plain")) != "plain" {
		t.Errorf("plain error = %q", User(errors.New("plain")))
	}
}

func TestUserRendersTheCause(t *testing.T) {
	e := Newf(KindNetwork, "llm", "cannot reach %s", "the provider")
	e.Err = errors.New("dial tcp: refused")
	got := User(e)
	if !contains(got, "cannot reach the provider") || !contains(got, "refused") {
		t.Errorf("User = %q", got)
	}
}

func TestErrorStringIncludesOperation(t *testing.T) {
	e := Newf(KindTimeout, "shell", "command timed out after %ds", 30)
	if e.Error() != "shell: command timed out after 30s" {
		t.Errorf("Error = %q", e.Error())
	}
}

func TestRetryableKinds(t *testing.T) {
	retryable := []Kind{KindNetwork, KindTimeout, KindRateLimit}
	for _, k := range retryable {
		if !Retryable(New(k, "x", "y")) {
			t.Errorf("%v should be retryable", k)
		}
	}
	notRetryable := []Kind{KindAuth, KindConfig, KindPermission, KindModel, KindParse, KindNotFound}
	for _, k := range notRetryable {
		if Retryable(New(k, "x", "y")) {
			t.Errorf("%v should not be retryable", k)
		}
	}
}

func TestIsKindAndHint(t *testing.T) {
	e := New(KindUsage, "cli", "bad flag")
	e.Hint = "try --help"
	if !IsKind(e, KindUsage) {
		t.Error("IsKind failed")
	}
	if Hint(e) != "try --help" {
		t.Errorf("Hint = %q", Hint(e))
	}
	if Hint(errors.New("x")) != "" {
		t.Error("a plain error has no hint")
	}
}

func TestWrapHint(t *testing.T) {
	base := errors.New("missing file")
	wrapped := WrapHint(KindNotFound, "config", "check the path", base)
	if Hint(wrapped) != "check the path" {
		t.Errorf("hint = %q", Hint(wrapped))
	}
	if WrapHint(KindNotFound, "config", "x", nil) != nil {
		t.Error("wrapping nil must return nil")
	}
	// A hint that is already set is preserved.
	withHint := New(KindAuth, "llm", "bad key")
	withHint.Hint = "original"
	again := WrapHint(KindAuth, "llm", "new", withHint)
	if Hint(again) != "original" {
		t.Errorf("hint = %q", Hint(again))
	}
}

func TestConstructors(t *testing.T) {
	cases := map[Kind]*Error{
		KindConfig:     Config("op", "msg %d", 1),
		KindUsage:      Usage("msg"),
		KindNotFound:   NotFound("op", "msg"),
		KindPermission: Permission("op", "msg"),
		KindInternal:   Internal("op", "msg"),
		KindParse:      Parse("op", "msg"),
		KindCancelled:  Cancelled("op", "msg"),
		KindNetwork:    Network("op", "msg"),
	}
	for want, err := range cases {
		if KindOf(err) != want {
			t.Errorf("constructor produced %v, want %v", KindOf(err), want)
		}
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
