package jobs

import "testing"

func TestRecoverJobPanicConvertsPanicToFailure(t *testing.T) {
	var gotErr error

	func() {
		defer recoverJobPanic(func(err error) { gotErr = err })
		panic("boom")
	}()

	if gotErr == nil {
		t.Fatal("expected recoverJobPanic to report an error, got nil")
	}
	if gotErr.Error() != "panic: boom" {
		t.Fatalf("unexpected error message: %q", gotErr.Error())
	}
}

func TestRecoverJobPanicNoOpWithoutPanic(t *testing.T) {
	called := false

	func() {
		defer recoverJobPanic(func(err error) { called = true })
	}()

	if called {
		t.Fatal("onFailed should not be called when there's no panic")
	}
}
