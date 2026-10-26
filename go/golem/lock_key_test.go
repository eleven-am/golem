package golem

import (
	"strings"
	"testing"
)

func TestNewLockKeyRefusesEmptyInvalidAndOversizedComponents(t *testing.T) {
	refused := map[string]func() (LockKey, error){
		"empty namespace":       func() (LockKey, error) { return NewLockKey("") },
		"empty part":            func() (LockKey, error) { return NewLockKey("budget", "artifact", "") },
		"invalid UTF-8":         func() (LockKey, error) { return NewLockKey("budget", "\xff") },
		"namespace over 256":    func() (LockKey, error) { return NewLockKey(strings.Repeat("n", 257)) },
		"parts total over 256":  func() (LockKey, error) { return NewLockKey(strings.Repeat("n", 128), strings.Repeat("p", 129)) },
		"invalid UTF-8 in name": func() (LockKey, error) { return NewLockKey("\xc3\x28") },
	}
	for name, build := range refused {
		if key, err := build(); err == nil || key != (LockKey{}) {
			t.Errorf("%s: key=%v err=%v; want a refusal and the zero key", name, key, err)
		}
	}
	if _, err := NewLockKey(strings.Repeat("n", 128), strings.Repeat("p", 128)); err != nil {
		t.Fatalf("a 256-byte key was refused: %v", err)
	}
	if _, err := NewLockKey("budget", "é", "\x00"); err != nil {
		t.Fatalf("valid UTF-8 was refused: %v", err)
	}
}

func TestLockKeyIdentityIsInjective(t *testing.T) {
	left, right := MustLockKey("ab", "c"), MustLockKey("a", "bc")
	if left == right {
		t.Fatal("different component boundaries produced the same lock key")
	}
	if MustLockKey("budget", "1") != MustLockKey("budget", "1") {
		t.Fatal("equal components produced different lock keys")
	}
	if MustLockKey("budget") == MustLockKey("budget", "budget") {
		t.Fatal("a namespace alone equals the namespace with a part")
	}
	identity, err := RuntimeLockKeyIdentity(left)
	if err != nil || identity == "" {
		t.Fatalf("identity=%q err=%v", identity, err)
	}
	if _, err := RuntimeLockKeyIdentity(LockKey{}); err == nil {
		t.Fatal("the zero lock key was accepted")
	}
}

func TestMustLockKeyPanicsOnARefusedKey(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("MustLockKey accepted an empty namespace")
		}
	}()
	MustLockKey("")
}
