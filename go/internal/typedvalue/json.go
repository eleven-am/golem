// Package typedvalue validates transport-neutral values against the compiled
// type contract. It performs no transport binding or schema construction.
package typedvalue

import (
	"bytes"
	"encoding/json"
	"github.com/eleven-am/golem/go/internal/bindingerr"
	"io"
	"unicode/utf8"
)

func invalidJSON() error {
	return bindingerr.New(bindingerr.KindInvalidValue, bindingerr.ValueMalformed, bindingerr.Type("JSON"))
}

type JSONLimits struct{ MaxBytes, MaxDepth, MaxNodes int }

func JSON(encoded []byte, limits JSONLimits) (any, error) {
	if limits.MaxBytes <= 0 {
		limits.MaxBytes = 512 << 10
	}
	if limits.MaxDepth <= 0 {
		limits.MaxDepth = 32
	}
	if limits.MaxNodes <= 0 {
		limits.MaxNodes = 32_768
	}
	if len(encoded) > limits.MaxBytes {
		return nil, bindingerr.New(bindingerr.KindInputLimit, bindingerr.LimitJSONBytes, bindingerr.Max(limits.MaxBytes))
	}
	if !utf8.Valid(encoded) {
		return nil, invalidJSON()
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, invalidJSON()
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, invalidJSON()
	}
	nodes := 0
	var walk func(any, int) error
	walk = func(value any, depth int) error {
		if depth > limits.MaxDepth {
			return bindingerr.New(bindingerr.KindInputLimit, bindingerr.LimitJSONDepth, bindingerr.Max(limits.MaxDepth))
		}
		nodes++
		if nodes > limits.MaxNodes {
			return bindingerr.New(bindingerr.KindInputLimit, bindingerr.LimitJSONNodes, bindingerr.Max(limits.MaxNodes))
		}
		switch item := value.(type) {
		case []any:
			for _, child := range item {
				if err := walk(child, depth+1); err != nil {
					return err
				}
			}
		case map[string]any:
			for _, child := range item {
				if err := walk(child, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(value, 1); err != nil {
		return nil, err
	}
	return value, nil
}
