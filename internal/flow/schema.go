package flow

import (
	"encoding/json"
	"fmt"
)

// The provider's structured-output guarantee is backed by local validation.
// Missing arrays (notably review findings) must never silently mean approval.
func validateShape(stage string, b []byte) error {
	var v any
	if e := json.Unmarshal(b, &v); e != nil {
		return e
	}
	return matchSchema(v, Schema(stage), "response")
}
func matchSchema(v any, s map[string]any, path string) error {
	switch s["type"] {
	case "string":
		if _, ok := v.(string); !ok {
			return fmt.Errorf("%s must be a string", path)
		}
	case "array":
		a, ok := v.([]any)
		if !ok {
			return fmt.Errorf("%s must be an array", path)
		}
		for i, x := range a {
			if e := matchSchema(x, s["items"].(map[string]any), fmt.Sprintf("%s[%d]", path, i)); e != nil {
				return e
			}
		}
	case "object":
		obj, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be an object", path)
		}
		props := s["properties"].(map[string]any)
		if len(obj) != len(props) {
			return fmt.Errorf("%s has missing or unknown fields", path)
		}
		for name, p := range props {
			x, ok := obj[name]
			if !ok {
				return fmt.Errorf("%s.%s is required", path, name)
			}
			if e := matchSchema(x, p.(map[string]any), path+"."+name); e != nil {
				return e
			}
		}
	default:
		return fmt.Errorf("unsupported schema type")
	}
	return nil
}
