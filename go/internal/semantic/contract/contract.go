// Package contract owns the canonical provider-extension payloads used by the
// compiler, provider lowerers, generated registries, and runtime.
package contract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

const (
	SpaceKind = "golem.semantic-space"
	IndexKind = "golem.semantic-index"
	Version   = 1
)

type Space struct {
	Name       string `json:"name"`
	Dimensions uint16 `json:"dimensions"`
}

type Index struct {
	Name       string   `json:"name"`
	Space      string   `json:"space"`
	Dimensions uint16   `json:"dimensions"`
	Fields     []string `json:"fields"`
	Metric     string   `json:"metric"`
}

func Encode(value any) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("semantic contract encode: %w", err)
	}
	return string(payload), nil
}

func DecodeSpace(payload string) (Space, error) {
	var result Space
	if err := strictDecode(payload, &result); err != nil {
		return Space{}, err
	}
	if result.Name == "" || result.Dimensions < 1 || result.Dimensions > 2000 {
		return Space{}, fmt.Errorf("semantic contract: invalid embedding space")
	}
	return result, nil
}

func DecodeIndex(payload string) (Index, error) {
	var result Index
	if err := strictDecode(payload, &result); err != nil {
		return Index{}, err
	}
	if result.Name == "" || result.Space == "" || result.Dimensions < 1 || result.Dimensions > 2000 || len(result.Fields) == 0 || result.Metric != "cosine" {
		return Index{}, fmt.Errorf("semantic contract: invalid semantic index")
	}
	for _, field := range result.Fields {
		if field == "" {
			return Index{}, fmt.Errorf("semantic contract: invalid semantic index field")
		}
	}
	return result, nil
}

func strictDecode(payload string, target any) error {
	decoder := json.NewDecoder(bytes.NewBufferString(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("semantic contract decode: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("semantic contract decode: trailing data")
	}
	encoded, err := json.Marshal(target)
	if err != nil || string(encoded) != payload {
		return fmt.Errorf("semantic contract decode: payload is not canonical")
	}
	return nil
}
