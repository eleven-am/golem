package golem

import (
	"encoding/json"
	"fmt"
)

type jsonInputTarget interface{ decodeJSONInput(any) error }

func (target *JSON[T]) decodeJSONInput(raw any) error {
	if target == nil {
		return fmt.Errorf("JSON input target is nil")
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	value, err := NewJSONDocument[T](encoded)
	if err != nil {
		return err
	}
	*target = value
	return nil
}

// RuntimeDecodeJSONInput recognizes a native JSON[T] target and builds its
// document from already-coerced JSON data. Numbers retain their exact value;
// ordinary registered input structs never use this JSON-specific codec.
func RuntimeDecodeJSONInput(target, raw any) (bool, error) {
	value, ok := target.(jsonInputTarget)
	if !ok {
		return false, nil
	}
	return true, value.decodeJSONInput(raw)
}
