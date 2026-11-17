// Package continuation owns authenticated, bounded traversal envelopes. A
// position is private traversal state, never evidence that a row is readable.
package continuation

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/eleven-am/golem/go/internal/pagecontract"
)

const (
	MaxPositionBytes = pagecontract.MaxCheckpointBytes
	MaxTokenBytes    = 8192
	MaxCandidates    = pagecontract.MaxCandidates
	maxKeys          = 8
	version          = 1
	maxBindingBytes  = 128 * 1024
)

var (
	ErrInvalid  = errors.New("invalid continuation")
	ErrExpired  = errors.New("continuation expired; restart traversal")
	ErrProgress = errors.New("continuation must advance within the candidate budget")
)

// Key material must be deployment-owned, shared by every serving instance and
// retained for decryption during rotation. Do not generate a key on process
// startup. Rotate before 2^32 encryptions across all instances sharing a key.
type Key struct {
	ID       string
	Material [32]byte
}

type Config struct {
	ActiveKey string
	Keys      []Key
	// MaxAge is an absolute chain lifetime, not a sliding page timeout. It must
	// be a whole number of seconds in [1 second, 24 hours].
	MaxAge time.Duration
}

// Binding receives already-canonical argument and admitted actor bytes from
// the operation owner. It deliberately does not serialize arbitrary Go values
// or reuse a policy cache key. Incarnation changes after data restoration.
type Binding struct {
	Contract    string
	Operation   string
	Revision    string
	Incarnation string
	Arguments   []byte
	Actor       []byte
}

type Scope struct {
	digest [32]byte
	valid  bool
}

func Bind(value Binding) (Scope, error) {
	parts := [][]byte{[]byte(value.Contract), []byte(value.Operation), []byte(value.Revision), []byte(value.Incarnation), value.Arguments, value.Actor}
	hash := sha256.New()
	hash.Write([]byte("golem/continuation/scope/1\x00"))
	for _, part := range parts {
		if len(part) == 0 || len(part) > maxBindingBytes {
			return Scope{}, fmt.Errorf("continuation binding components must be nonempty and bounded")
		}
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(part)))
		hash.Write(size[:])
		hash.Write(part)
	}
	var scope Scope
	copy(scope.digest[:], hash.Sum(nil))
	scope.valid = true
	return scope, nil
}

// Codec is immutable after construction and safe for concurrent requests.
// Replacing it with a new keyring rotates the writer without retaining caller
// contexts, keys supplied by mutable configuration, or traversal sessions.
type Codec struct {
	active string
	keys   map[string]cipher.AEAD
	maxAge int64
	now    func() time.Time
}

func New(config Config) (*Codec, error) {
	if config.MaxAge < time.Second || config.MaxAge > 24*time.Hour || config.MaxAge%time.Second != 0 || len(config.Keys) < 1 || len(config.Keys) > maxKeys {
		return nil, fmt.Errorf("continuation keyring or absolute lifetime is invalid")
	}
	codec := &Codec{active: config.ActiveKey, keys: make(map[string]cipher.AEAD, len(config.Keys)), maxAge: int64(config.MaxAge / time.Second), now: time.Now}
	materials := make(map[[32]byte]bool, len(config.Keys))
	for _, key := range config.Keys {
		if !validKeyID(key.ID) || codec.keys[key.ID] != nil || key.Material == ([32]byte{}) || materials[key.Material] {
			return nil, fmt.Errorf("continuation keys require unique identifiers and nonzero, distinct 256-bit material")
		}
		block, err := aes.NewCipher(key.Material[:])
		if err != nil {
			return nil, err
		}
		aead, err := cipher.NewGCMWithRandomNonce(block)
		if err != nil {
			return nil, err
		}
		codec.keys[key.ID] = aead
		materials[key.Material] = true
	}
	if codec.keys[codec.active] == nil {
		return nil, fmt.Errorf("continuation active key is not registered")
	}
	return codec, nil
}

func validKeyID(id string) bool {
	if len(id) < 1 || len(id) > 32 {
		return false
	}
	for _, char := range []byte(id) {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' || char == '-') {
			return false
		}
	}
	return true
}

// Cursor can only originate in Begin or an authenticated Resume. Its state is
// copied on ingress and egress. Consumed counts candidates, including rows later
// withheld by authorization or output gates, rather than emitted items.
type Cursor struct {
	owner    *Codec
	scope    Scope
	issued   int64
	expires  int64
	consumed uint64
	position []byte
}

func (cursor Cursor) Position() []byte   { return bytes.Clone(cursor.position) }
func (cursor Cursor) Consumed() uint64   { return cursor.consumed }
func (cursor Cursor) Expires() time.Time { return time.Unix(cursor.expires, 0).UTC() }

func (codec *Codec) Begin(scope Scope) (Cursor, error) {
	if codec == nil || codec.now == nil || codec.maxAge < 1 || codec.keys[codec.active] == nil || !scope.valid {
		return Cursor{}, ErrInvalid
	}
	now := codec.now().Unix()
	if now < 0 || now > math.MaxInt64-codec.maxAge {
		return Cursor{}, ErrInvalid
	}
	return Cursor{owner: codec, scope: scope, issued: now, expires: now + codec.maxAge}, nil
}

func associated(header []byte, scope Scope) []byte {
	value := append([]byte("golem/continuation/envelope/1\x00"), header...)
	return append(value, scope.digest[:]...)
}

func (codec *Codec) Resume(scope Scope, token string) (Cursor, error) {
	if codec == nil || !scope.valid || len(token) == 0 || len(token) > MaxTokenBytes {
		return Cursor{}, ErrInvalid
	}
	encoded, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || base64.RawURLEncoding.EncodeToString(encoded) != token || len(encoded) < 3 || encoded[0] != version {
		return Cursor{}, ErrInvalid
	}
	headerSize := 2 + int(encoded[1])
	if headerSize > len(encoded) || !validKeyID(string(encoded[2:headerSize])) {
		return Cursor{}, ErrInvalid
	}
	aead := codec.keys[string(encoded[2:headerSize])]
	if aead == nil || len(encoded)-headerSize > 24+MaxPositionBytes+aead.Overhead() {
		return Cursor{}, ErrInvalid
	}
	plain, err := aead.Open(nil, nil, encoded[headerSize:], associated(encoded[:headerSize], scope))
	if err != nil || len(plain) <= 24 {
		return Cursor{}, ErrInvalid
	}
	cursor := Cursor{owner: codec, scope: scope, issued: int64(binary.BigEndian.Uint64(plain[:8])), expires: int64(binary.BigEndian.Uint64(plain[8:16])), consumed: binary.BigEndian.Uint64(plain[16:24]), position: bytes.Clone(plain[24:])}
	if cursor.issued < 0 || cursor.expires <= cursor.issued || cursor.expires-cursor.issued > codec.maxAge || cursor.consumed == 0 {
		return Cursor{}, ErrInvalid
	}
	if err := codec.current(cursor); err != nil {
		return Cursor{}, err
	}
	return cursor, nil
}

func (codec *Codec) current(cursor Cursor) error {
	if codec == nil || cursor.owner != codec || !cursor.scope.valid {
		return ErrInvalid
	}
	now := codec.now().Unix()
	if now < cursor.issued {
		return ErrInvalid
	}
	if now >= cursor.expires {
		return ErrExpired
	}
	return nil
}

// CheckAdvance validates a nonterminal checkpoint without issuing a token.
// The page owner uses the same checks before output hydration/encoding and
// again at issuance, when the fixed deadline may have expired.
func (codec *Codec) CheckAdvance(cursor Cursor, position []byte, consumed uint32) error {
	if err := codec.current(cursor); err != nil {
		return err
	}
	if len(position) == 0 || len(position) > MaxPositionBytes || bytes.Equal(position, cursor.position) || consumed == 0 || consumed > MaxCandidates || cursor.consumed > math.MaxUint64-uint64(consumed) {
		return ErrProgress
	}
	return nil
}

// Advance emits only a nonterminal checkpoint with positive bounded candidate
// consumption and different private state. Replaying an input token is allowed
// for retries. The operation must additionally enforce its ordering contract:
// opaque bytes cannot prove that an arbitrary keyset or snapshot advanced.
func (codec *Codec) Advance(cursor Cursor, position []byte, consumed uint32) (string, error) {
	if err := codec.CheckAdvance(cursor, position, consumed); err != nil {
		return "", err
	}
	header := append([]byte{version, byte(len(codec.active))}, codec.active...)
	plain := binary.BigEndian.AppendUint64(nil, uint64(cursor.issued))
	plain = binary.BigEndian.AppendUint64(plain, uint64(cursor.expires))
	plain = binary.BigEndian.AppendUint64(plain, cursor.consumed+uint64(consumed))
	plain = append(plain, position...)
	sealed := codec.keys[codec.active].Seal(nil, nil, plain, associated(header, cursor.scope))
	return base64.RawURLEncoding.EncodeToString(append(header, sealed...)), nil
}
