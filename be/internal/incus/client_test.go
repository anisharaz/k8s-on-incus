package incus

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCallWithContextReturnsResultWhenFasterThanContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	val, err := callWithContext(ctx, func() (int, error) {
		return 42, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != 42 {
		t.Fatalf("expected 42, got %d", val)
	}
}

func TestCallWithContextPropagatesFnError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	wantErr := errors.New("boom")
	_, err := callWithContext(ctx, func() (int, error) {
		return 0, wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected %v, got %v", wantErr, err)
	}
}

func TestCallWithContextReturnsCtxErrOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	block := make(chan struct{})
	defer close(block)

	_, err := callWithContext(ctx, func() (int, error) {
		<-block // never returns before the test ends
		return 1, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}
