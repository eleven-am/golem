package ir

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const maxCanonicalContractBytes = 16 << 20

// decodeCurrentContractCanonicalFraming checks only the current versioned JSON
// framing and byte-for-byte canonical encoding. It is deliberately private and
// non-authoritative: semantic authority requires the complete CompilationIR
// compiler validation, including ModelIR/ContractIR agreement. No persisted or
// runtime input may enter through this framing test seam.
func decodeCurrentContractCanonicalFraming(payload []byte) (ContractIR, error) {
	if err := validateContractJSONEnvelope(payload, ContractFormatVersion); err != nil {
		return ContractIR{}, err
	}
	var contract ContractIR
	if err := decodeExactContractJSON(payload, &contract); err != nil {
		return ContractIR{}, fmt.Errorf("contract IR decode: %w", err)
	}
	canonical, err := CanonicalContract(contract)
	if err != nil {
		return ContractIR{}, fmt.Errorf("contract IR decode: canonicalize: %w", err)
	}
	if !bytes.Equal(canonical, payload) {
		return ContractIR{}, fmt.Errorf("contract IR decode: document is not in canonical normalized form")
	}
	return contract, nil
}

func validateContractJSONEnvelope(payload []byte, expected uint16) error {
	if len(payload) == 0 {
		return fmt.Errorf("contract IR decode: empty document")
	}
	if len(payload) > maxCanonicalContractBytes {
		return fmt.Errorf("contract IR decode: document exceeds %d bytes", maxCanonicalContractBytes)
	}
	if err := rejectDuplicateJSONKeys(payload); err != nil {
		return fmt.Errorf("contract IR decode: %w", err)
	}
	var envelope struct {
		FormatVersion uint16 `json:"formatVersion"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(&envelope); err != nil {
		return fmt.Errorf("contract IR decode: format version: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return fmt.Errorf("contract IR decode: format version: %w", err)
	}
	if envelope.FormatVersion != expected {
		return fmt.Errorf("contract IR version %d is unsupported; expected %d", envelope.FormatVersion, expected)
	}
	return nil
}

func decodeExactContractJSON(payload []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	return requireJSONEOF(decoder)
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("trailing JSON value")
		}
		return fmt.Errorf("trailing data: %w", err)
	}
	return nil
}

func rejectDuplicateJSONKeys(payload []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > 1024 {
			return fmt.Errorf("JSON nesting exceeds 1024")
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, composite := token.(json.Delim)
		if !composite {
			return nil
		}
		switch delimiter {
		case '{':
			seen := map[string]struct{}{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return fmt.Errorf("object key is not a string")
				}
				if _, duplicate := seen[key]; duplicate {
					return fmt.Errorf("duplicate JSON object key %q", key)
				}
				seen[key] = struct{}{}
				if err := value(depth + 1); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil {
				return err
			}
			if closing != json.Delim('}') {
				return fmt.Errorf("object is not closed")
			}
		case '[':
			for decoder.More() {
				if err := value(depth + 1); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil {
				return err
			}
			if closing != json.Delim(']') {
				return fmt.Errorf("array is not closed")
			}
		default:
			return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
		}
		return nil
	}
	if err := value(0); err != nil {
		return err
	}
	return requireJSONEOF(decoder)
}
