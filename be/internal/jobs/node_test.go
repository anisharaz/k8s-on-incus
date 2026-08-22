package jobs

import (
	"strings"
	"testing"
)

func TestGenerateSSHPasswordLengthAndAlphabet(t *testing.T) {
	password, err := generateSSHPassword(16)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(password) != 16 {
		t.Fatalf("expected length 16, got %d (%q)", len(password), password)
	}
	for _, r := range password {
		if !strings.ContainsRune(sshPasswordAlphabet, r) {
			t.Fatalf("password %q contains character %q outside the allowed alphabet", password, r)
		}
	}
}

func TestGenerateSSHPasswordIsRandom(t *testing.T) {
	a, err := generateSSHPassword(16)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, err := generateSSHPassword(16)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a == b {
		t.Fatalf("expected two independently generated passwords to differ, both were %q", a)
	}
}
