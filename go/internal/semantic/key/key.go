// Package key owns the closed canonical identity encoding shared by source
// scans and authorized typed rows. Keys are opaque and never leave Golem's
// managed semantic storage boundary.
package key

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"
)

const maximumInlineBytes = 512
const keyPrefix = "golem-semantic-key:v1"

func Encode(values []any) (string, error) {
	if len(values) == 0 {
		return "", fmt.Errorf("semantic key: identity is empty")
	}
	var result strings.Builder
	result.WriteString(keyPrefix)
	for _, value := range values {
		encoded, err := encodeValue(value)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&result, "|%d:", len(encoded))
		result.WriteString(encoded)
	}
	if result.Len() > maximumInlineBytes {
		digest := sha256.Sum256([]byte(result.String()))
		return keyPrefix + "|sha256:" + hex.EncodeToString(digest[:]), nil
	}
	return result.String(), nil
}

func encodeValue(value any) (string, error) {
	if value == nil {
		return "", fmt.Errorf("semantic key: primary identity contains null")
	}
	if instant, ok := value.(time.Time); ok {
		return "t:" + instant.UTC().Format(time.RFC3339Nano), nil
	}
	reflected := reflect.ValueOf(value)
	for reflected.Kind() == reflect.Pointer {
		if reflected.IsNil() {
			return "", fmt.Errorf("semantic key: primary identity contains null")
		}
		reflected = reflected.Elem()
	}
	switch reflected.Kind() {
	case reflect.String:
		text := reflected.String()
		if uuid, ok := canonicalUUID(text); ok {
			return "uuid:" + uuid, nil
		}
		return "s:" + strconv.Itoa(len(text)) + ":" + text, nil
	case reflect.Bool:
		if reflected.Bool() {
			return "b:1", nil
		}
		return "b:0", nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return "i:" + strconv.FormatInt(reflected.Int(), 10), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "u:" + strconv.FormatUint(reflected.Uint(), 10), nil
	case reflect.Float32, reflect.Float64:
		return "f:" + strconv.FormatFloat(reflected.Float(), 'g', -1, reflected.Type().Bits()), nil
	case reflect.Slice:
		if reflected.Type().Elem().Kind() == reflect.Uint8 {
			bytes := reflected.Bytes()
			if len(bytes) == 16 {
				return "uuid:" + hex.EncodeToString(bytes), nil
			}
			return "x:" + hex.EncodeToString(bytes), nil
		}
	case reflect.Array:
		if reflected.Type().Elem().Kind() == reflect.Uint8 {
			bytes := make([]byte, reflected.Len())
			for index := range bytes {
				bytes[index] = byte(reflected.Index(index).Uint())
			}
			if len(bytes) == 16 {
				return "uuid:" + hex.EncodeToString(bytes), nil
			}
			return "x:" + hex.EncodeToString(bytes), nil
		}
	}
	if stringer, ok := value.(fmt.Stringer); ok {
		text := stringer.String()
		return "v:" + strconv.Itoa(len(text)) + ":" + text, nil
	}
	return "", fmt.Errorf("semantic key: unsupported primary identity type %T", value)
}

func canonicalUUID(value string) (string, bool) {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return "", false
	}
	compact := make([]byte, 0, 32)
	for index := 0; index < len(value); index++ {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		digit := value[index]
		if !(digit >= '0' && digit <= '9' || digit >= 'a' && digit <= 'f') {
			return "", false
		}
		compact = append(compact, digit)
	}
	return string(compact), true
}
