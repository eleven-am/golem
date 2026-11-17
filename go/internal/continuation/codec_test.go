package continuation

import (
	"bytes"
	"encoding/base64"
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

func testConfig() Config {
	return Config{ActiveKey: "current", Keys: []Key{{ID: "current", Material: [32]byte{1}}, {ID: "retiring", Material: [32]byte{2}}}, MaxAge: 15 * time.Minute}
}

func testBinding() Binding {
	return Binding{Contract: "contract", Operation: "home", Revision: "rank-1", Incarnation: "database-1", Arguments: []byte(`{"take":25}`), Actor: []byte(`{"account":1,"profile":2,"revision":3}`)}
}

func testCodec(t *testing.T) (*Codec, Scope, *time.Time) {
	t.Helper()
	codec, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1800000000, 0)
	codec.now = func() time.Time { return now }
	scope, err := Bind(testBinding())
	if err != nil {
		t.Fatal(err)
	}
	return codec, scope, &now
}

func firstToken(t *testing.T, codec *Codec, scope Scope) string {
	t.Helper()
	cursor, err := codec.Begin(scope)
	if err != nil {
		t.Fatal(err)
	}
	token, err := codec.Advance(cursor, []byte("private-snapshot/ordinal-25"), 25)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestRoundTripRetryAndAbsoluteExpiry(t *testing.T) {
	codec, scope, now := testCodec(t)
	token := firstToken(t, codec, scope)
	first, err := codec.Resume(scope, token)
	if err != nil || first.Consumed() != 25 || string(first.Position()) != "private-snapshot/ordinal-25" {
		t.Fatalf("first checkpoint: %+v %v", first, err)
	}
	// Retryable reads reauthorize externally; the token is not consumed here.
	if _, err := codec.Resume(scope, token); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(14 * time.Minute)
	nextToken, err := codec.Advance(first, []byte("private-snapshot/ordinal-50"), 25)
	if err != nil {
		t.Fatal(err)
	}
	next, err := codec.Resume(scope, nextToken)
	if err != nil || next.Consumed() != 50 || !next.Expires().Equal(first.Expires()) {
		t.Fatalf("next checkpoint renewed the chain: %+v %v", next, err)
	}
	*now = first.Expires()
	for _, encoded := range []string{token, nextToken} {
		if _, err := codec.Resume(scope, encoded); !errors.Is(err, ErrExpired) {
			t.Fatalf("expired token admitted: %v", err)
		}
	}
	if _, err := codec.Advance(next, []byte("later"), 1); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired cursor advanced: %v", err)
	}
}

func TestEveryBindingComponentIsAuthenticated(t *testing.T) {
	codec, scope, _ := testCodec(t)
	token := firstToken(t, codec, scope)
	cases := map[string]func(*Binding){
		"contract":               func(b *Binding) { b.Contract += "2" },
		"operation":              func(b *Binding) { b.Operation += "2" },
		"revision":               func(b *Binding) { b.Revision += "2" },
		"incarnation":            func(b *Binding) { b.Incarnation += "2" },
		"arguments":              func(b *Binding) { b.Arguments = []byte(`{"take":24}`) },
		"actor":                  func(b *Binding) { b.Actor = []byte(`{"account":1,"profile":4,"revision":3}`) },
		"authorization revision": func(b *Binding) { b.Actor = []byte(`{"account":1,"profile":2,"revision":4}`) },
		"boundary ambiguity":     func(b *Binding) { b.Contract, b.Operation = "contrac", "thome" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			binding := testBinding()
			mutate(&binding)
			other, err := Bind(binding)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := codec.Resume(other, token); !errors.Is(err, ErrInvalid) {
				t.Fatalf("wrong binding admitted: %v", err)
			}
		})
	}
}

func TestEnvelopeTamperAndMalformedInput(t *testing.T) {
	codec, scope, _ := testCodec(t)
	token := firstToken(t, codec, scope)
	raw, _ := base64.RawURLEncoding.DecodeString(token)
	if bytes.Contains(raw, []byte("private-snapshot")) {
		t.Fatal("traversal state exposed in token")
	}
	for index := range raw {
		changed := bytes.Clone(raw)
		changed[index] ^= 1
		if _, err := codec.Resume(scope, base64.RawURLEncoding.EncodeToString(changed)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("tampered byte %d admitted: %v", index, err)
		}
	}
	for _, bad := range []string{"", "!", token + "=", token + "\n", token[:len(token)-1], strings.Repeat("A", MaxTokenBytes+1)} {
		if _, err := codec.Resume(scope, bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("malformed token admitted: %v", err)
		}
	}
	if other := firstToken(t, codec, scope); other == token {
		t.Fatal("independent encryption reused nonce/ciphertext")
	}
}

func TestRotationRestartAndConfigurationOwnership(t *testing.T) {
	codec, scope, now := testCodec(t)
	token := firstToken(t, codec, scope)
	config := testConfig()
	config.ActiveKey = "new"
	config.Keys = append(config.Keys, Key{ID: "new", Material: [32]byte{3}})
	rotated, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	rotated.now = func() time.Time { return *now }
	config.Keys[0].Material = [32]byte{} // no aliased application key storage
	old, err := rotated.Resume(scope, token)
	if err != nil {
		t.Fatalf("old reader key lost during rotation/restart: %v", err)
	}
	newToken, err := rotated.Advance(old, []byte("ordinal-26"), 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := codec.Resume(scope, newToken); !errors.Is(err, ErrInvalid) {
		t.Fatalf("old-only process admitted new key: %v", err)
	}
	config = testConfig()
	config.ActiveKey, config.Keys = "new", []Key{{ID: "new", Material: [32]byte{3}}}
	newOnly, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	newOnly.now = rotated.now
	if _, err := newOnly.Resume(scope, newToken); err != nil {
		t.Fatalf("independent restarted process lost new token: %v", err)
	}
	if _, err := newOnly.Resume(scope, token); !errors.Is(err, ErrInvalid) {
		t.Fatalf("revoked key still admitted: %v", err)
	}
}

func TestProgressBoundsAndOwnedCursor(t *testing.T) {
	codec, scope, _ := testCodec(t)
	cursor, err := codec.Resume(scope, firstToken(t, codec, scope))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		position []byte
		count    uint32
	}{
		{nil, 1}, {[]byte{}, 1}, {cursor.Position(), 1}, {[]byte("next"), 0},
		{[]byte("next"), MaxCandidates + 1}, {make([]byte, MaxPositionBytes+1), 1},
	} {
		if _, err := codec.Advance(cursor, test.position, test.count); !errors.Is(err, ErrProgress) {
			t.Fatalf("invalid progress admitted: %v", err)
		}
	}
	overflow := cursor
	overflow.consumed = math.MaxUint64
	if _, err := codec.Advance(overflow, []byte("next"), 1); !errors.Is(err, ErrProgress) {
		t.Fatalf("candidate count overflow admitted: %v", err)
	}
	other, _, _ := testCodec(t)
	if _, err := other.Advance(cursor, []byte("next"), 1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("foreign cursor admitted: %v", err)
	}
	copy := cursor.Position()
	copy[0] ^= 1
	if bytes.Equal(cursor.Position(), copy) {
		t.Fatal("position leaked mutable state")
	}
	position := bytes.Repeat([]byte{1}, MaxPositionBytes)
	token, err := codec.Advance(cursor, position, MaxCandidates)
	if err != nil || len(token) > MaxTokenBytes {
		t.Fatalf("maximum checkpoint refused: len=%d err=%v", len(token), err)
	}
	position[0] = 2
	resumed, err := codec.Resume(scope, token)
	if err != nil || resumed.Position()[0] != 1 || resumed.Consumed() != 25+MaxCandidates {
		t.Fatalf("maximum checkpoint corrupted: %v", err)
	}
}

func TestConfigurationAndUninitializedRefusal(t *testing.T) {
	for _, mutate := range []func(*Config){
		func(c *Config) { c.Keys = nil }, func(c *Config) { c.ActiveKey = "missing" },
		func(c *Config) { c.MaxAge = 0 }, func(c *Config) { c.MaxAge = time.Second / 2 },
		func(c *Config) { c.MaxAge = 25 * time.Hour }, func(c *Config) { c.Keys[0].Material = [32]byte{} },
		func(c *Config) { c.Keys[1].ID = c.Keys[0].ID }, func(c *Config) { c.Keys[1].Material = c.Keys[0].Material },
		func(c *Config) { c.Keys[0].ID = "../key" }, func(c *Config) { c.Keys[0].ID = strings.Repeat("a", 33) },
		func(c *Config) { c.Keys = append(c.Keys, make([]Key, maxKeys)...) },
	} {
		config := testConfig()
		mutate(&config)
		if _, err := New(config); err == nil {
			t.Fatal("invalid configuration accepted")
		}
	}
	codec, scope, now := testCodec(t)
	for _, empty := range []*Codec{nil, {}} {
		if _, err := empty.Begin(scope); !errors.Is(err, ErrInvalid) {
			t.Fatalf("uninitialized codec admitted: %v", err)
		}
	}
	if _, err := codec.Begin(Scope{}); !errors.Is(err, ErrInvalid) {
		t.Fatal("uninitialized scope admitted")
	}
	binding := testBinding()
	binding.Actor = nil
	if _, err := Bind(binding); err == nil {
		t.Fatal("empty binding accepted")
	}
	binding.Actor = make([]byte, maxBindingBytes+1)
	if _, err := Bind(binding); err == nil {
		t.Fatal("unbounded binding accepted")
	}
	token := firstToken(t, codec, scope)
	*now = now.Add(-time.Second)
	if _, err := codec.Resume(scope, token); !errors.Is(err, ErrInvalid) {
		t.Fatalf("future-issued token admitted: %v", err)
	}
}
