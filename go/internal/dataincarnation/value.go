// Package dataincarnation owns the durable provider identity vocabulary.
package dataincarnation

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

const (
	Capability        = "data-incarnation-v1"
	Kind              = "golem.data-incarnation"
	Version    uint16 = 1
)

// Value is an opaque durable identity. Its zero value is invalid.
type Value struct{ bytes [16]byte }

func (value Value) Valid() bool { return value.bytes != [16]byte{} }
func (value Value) String() string {
	if !value.Valid() {
		return ""
	}
	return hex.EncodeToString(value.bytes[:])
}

func Parse(text string) (Value, error) {
	var value Value
	if len(text) != 32 {
		return value, fmt.Errorf("data incarnation requires 32 lowercase hexadecimal characters")
	}
	if _, err := hex.Decode(value.bytes[:], []byte(text)); err != nil || !value.Valid() || value.String() != text {
		return Value{}, fmt.Errorf("data incarnation is not canonical nonzero hexadecimal")
	}
	return value, nil
}

func New() (Value, error) {
	for {
		var value Value
		if _, err := rand.Read(value.bytes[:]); err != nil {
			return Value{}, err
		}
		if value.Valid() {
			return value, nil
		}
	}
}
