package contract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

const (
	IndexKind = "golem.fulltext-index"
	Version   = 1
)

const (
	FoldingDiacritics = "diacritics"
	FoldingNone       = "none"
)

type Field struct {
	ID     string  `json:"id"`
	Weight float64 `json:"weight"`
}

func ExportedIndexName(value string) (string, bool) {
	var result strings.Builder
	upper := true
	for _, character := range value {
		if character == '-' || character == '_' {
			upper = true
			continue
		}
		if !unicode.IsLetter(character) && !unicode.IsDigit(character) {
			return "", false
		}
		if upper {
			character = unicode.ToUpper(character)
		}
		result.WriteRune(character)
		upper = false
	}
	return result.String(), result.Len() != 0
}

type Index struct {
	Name    string  `json:"name"`
	Fields  []Field `json:"fields"`
	Folding string  `json:"folding"`
	Prefix  []uint8 `json:"prefix"`
}

func IndexesByModel(model ir.ModelIR) (map[ir.ModelID][]Index, error) {
	result := make(map[ir.ModelID][]Index)
	seen := make(map[string]string)
	for _, extension := range model.Extensions {
		if extension.Kind != IndexKind {
			continue
		}
		index, err := Decode(extension.Payload)
		if err != nil {
			return nil, fmt.Errorf("full-text contract: invalid index extension: %w", err)
		}
		owner := ir.ModelID(extension.Owner)
		key := string(owner) + "\x00" + index.Name
		payload, _ := Encode(index)
		if previous, duplicate := seen[key]; duplicate {
			if previous != payload {
				return nil, fmt.Errorf("full-text contract: provider definitions differ for model %s index %q", owner, index.Name)
			}
			continue
		}
		seen[key] = payload
		result[owner] = append(result[owner], index)
	}
	for owner := range result {
		sort.Slice(result[owner], func(i, j int) bool { return result[owner][i].Name < result[owner][j].Name })
	}
	return result, nil
}

func Encode(index Index) (string, error) {
	if err := validate(index); err != nil {
		return "", err
	}
	payload, err := json.Marshal(index)
	if err != nil {
		return "", fmt.Errorf("full-text contract encode: %w", err)
	}
	return string(payload), nil
}

func Decode(payload string) (Index, error) {
	decoder := json.NewDecoder(bytes.NewBufferString(payload))
	decoder.DisallowUnknownFields()
	var result Index
	if err := decoder.Decode(&result); err != nil {
		return Index{}, fmt.Errorf("full-text contract decode: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Index{}, fmt.Errorf("full-text contract decode: trailing data")
	}
	if err := validate(result); err != nil {
		return Index{}, err
	}
	encoded, err := json.Marshal(result)
	if err != nil || string(encoded) != payload {
		return Index{}, fmt.Errorf("full-text contract decode: payload is not canonical")
	}
	return result, nil
}

func validate(index Index) error {
	if index.Name == "" || len(index.Fields) == 0 || index.Folding != FoldingDiacritics && index.Folding != FoldingNone {
		return fmt.Errorf("full-text contract: invalid index")
	}
	seenFields := make(map[string]bool, len(index.Fields))
	for _, field := range index.Fields {
		if field.ID == "" || strings.ContainsAny(field.ID, "\x00,") || field.Weight <= 0 || seenFields[field.ID] {
			return fmt.Errorf("full-text contract: invalid field")
		}
		seenFields[field.ID] = true
	}
	seenPrefix := make(map[uint8]bool, len(index.Prefix))
	for position, length := range index.Prefix {
		if length < 1 || length > 32 || seenPrefix[length] || position > 0 && index.Prefix[position-1] >= length {
			return fmt.Errorf("full-text contract: invalid prefix length")
		}
		seenPrefix[length] = true
	}
	return nil
}
