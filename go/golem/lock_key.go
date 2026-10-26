package golem

import (
	"encoding/binary"
	"fmt"
	"unicode/utf8"
)

const maxLockKeyBytes = 256

type LockKey struct{ identity string }

func NewLockKey(namespace string, parts ...string) (LockKey, error) {
	components := append([]string{namespace}, parts...)
	total := 0
	identity := make([]byte, 0, len(components)*2+len(namespace))
	for index, component := range components {
		if component == "" {
			return LockKey{}, fmt.Errorf("P4_LOCK_KEY: component %d is empty", index)
		}
		if !utf8.ValidString(component) {
			return LockKey{}, fmt.Errorf("P4_LOCK_KEY: component %d is not valid UTF-8", index)
		}
		total += len(component)
		if total > maxLockKeyBytes {
			return LockKey{}, fmt.Errorf("P4_LOCK_KEY: lock key exceeds %d bytes", maxLockKeyBytes)
		}
		identity = binary.AppendUvarint(identity, uint64(len(component)))
		identity = append(identity, component...)
	}
	return LockKey{identity: string(identity)}, nil
}

func MustLockKey(namespace string, parts ...string) LockKey {
	key, err := NewLockKey(namespace, parts...)
	if err != nil {
		panic(err)
	}
	return key
}

func RuntimeLockKeyIdentity(key LockKey) (string, error) {
	if key.identity == "" {
		return "", fmt.Errorf("P4_LOCK_KEY: lock key was not built by NewLockKey")
	}
	return key.identity, nil
}
