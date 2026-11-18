package box

import (
	"errors"
	"strings"
	"testing"
)

func TestCloseRecoveringPanicKeepsThePanicAsTheCleanupError(t *testing.T) {
	var recovered any
	err := closeRecoveringPanic(func() error { panic("close blew up") }, func(v any) { recovered = v })
	if err == nil || !strings.Contains(err.Error(), "close blew up") {
		t.Fatalf("a panicking cleanup must report an error, got %v", err)
	}
	if recovered != "close blew up" {
		t.Fatalf("onPanic not called with the panic value, got %v", recovered)
	}
}

func TestCloseRecoveringPanicReturnsTheCloseResult(t *testing.T) {
	want := errors.New("close failed")
	if got := closeRecoveringPanic(func() error { return want }, nil); !errors.Is(got, want) {
		t.Fatalf("want %v, got %v", want, got)
	}
	if got := closeRecoveringPanic(func() error { return nil }, nil); got != nil {
		t.Fatalf("a clean close reports nil, got %v", got)
	}
}
