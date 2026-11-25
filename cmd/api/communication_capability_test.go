package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

type failingEntropyReader struct{}

func (failingEntropyReader) Read([]byte) (int, error) {
	return 0, errors.New("entropy source unavailable")
}

func TestNewCallRoomFromUsesCryptographicEntropy(t *testing.T) {
	entropy := bytes.Repeat([]byte{0x5a}, 16)
	room, err := newCallRoomFrom(bytes.NewReader(entropy))
	if err != nil {
		t.Fatalf("newCallRoomFrom returned error: %v", err)
	}
	if !strings.HasPrefix(room, "testagram-") {
		t.Fatalf("unexpected room prefix: %q", room)
	}
	if got, want := len(strings.TrimPrefix(room, "testagram-")), 32; got != want {
		t.Fatalf("room entropy hex length = %d, want %d", got, want)
	}
	if room != "testagram-5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a" {
		t.Fatalf("unexpected deterministic room value: %q", room)
	}
}

func TestNewCallRoomFromFailsClosedWithoutEntropy(t *testing.T) {
	room, err := newCallRoomFrom(failingEntropyReader{})
	if err == nil {
		t.Fatal("expected entropy failure")
	}
	if room != "" {
		t.Fatalf("must not return a fallback room identifier, got %q", room)
	}
}

func TestNewCallRoomReturnsUniqueIdentifiers(t *testing.T) {
	first := newCallRoom()
	if first == "" {
		t.Fatal("first call room generation failed")
	}
	second := newCallRoom()
	if second == "" {
		t.Fatal("second call room generation failed")
	}
	if first == second {
		t.Fatal("call room identifiers must be unique")
	}
}

func TestUnverifiedCommunicationEncryptionStateFailsClosed(t *testing.T) {
	enabled, epoch := unverifiedCommunicationEncryptionState()
	if enabled {
		t.Fatal("unverified communication encryption must never be reported as enabled")
	}
	if epoch != 0 {
		t.Fatalf("unverified communication encryption epoch = %d, want 0", epoch)
	}
}
