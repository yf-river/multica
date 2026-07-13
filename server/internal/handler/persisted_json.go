package handler

import (
	"encoding/json"
	"fmt"
)

func decodeJSONArray(raw []byte, field string) ([]any, error) {
	var value []any
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return nil, fmt.Errorf("%s must be a JSON array", field)
	}
	return value, nil
}

func decodeJSONObject(raw []byte, field string) (map[string]any, error) {
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return nil, fmt.Errorf("%s must be a JSON object", field)
	}
	return value, nil
}

func mustDecodePersistedJSONObject(raw []byte, field string) map[string]any {
	value, err := decodeJSONObject(raw, field)
	if err != nil {
		panic("handler: " + err.Error())
	}
	return value
}

func mustDecodePersistedJSONArray(raw []byte, field string) []any {
	value, err := decodeJSONArray(raw, field)
	if err != nil {
		panic("handler: " + err.Error())
	}
	return value
}

func mustDecodePersistedJSONValue(raw []byte, field string) any {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		panic("handler: " + field + " must be valid JSON: " + err.Error())
	}
	return value
}
