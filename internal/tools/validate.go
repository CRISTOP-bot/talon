package tools

import (
	"fmt"
	"math"
	"reflect"
	"regexp"
	"strings"
)

// Validate checks arguments against the JSON schema subset Talon uses:
// type checks, required fields, enum values and simple numeric bounds.
func Validate(schema map[string]any, args map[string]any) error {
	if schema == nil {
		return nil
	}
	if schema["type"] != nil && schema["type"] != "object" {
		return fmt.Errorf("schema must describe an object")
	}
	props, _ := schema["properties"].(map[string]any)
	required := toStringList(schema["required"])

	for _, name := range required {
		if _, ok := args[name]; !ok {
			return fmt.Errorf("missing required argument %q", name)
		}
	}
	for key, value := range args {
		spec, ok := props[key].(map[string]any)
		if !ok {
			continue
		}
		if value == nil {
			continue
		}
		if err := validateValue(key, spec, value); err != nil {
			return err
		}
	}
	return nil
}

func validateValue(name string, spec map[string]any, value any) error {
	want, _ := spec["type"].(string)
	if want != "" {
		if err := checkType(name, want, value); err != nil {
			return err
		}
	}
	if enum, ok := spec["enum"].([]any); ok && len(enum) > 0 {
		got := fmt.Sprint(value)
		found := false
		for _, e := range enum {
			if fmt.Sprint(e) == got {
				found = true
				break
			}
		}
		if !found {
			options := make([]string, 0, len(enum))
			for _, e := range enum {
				options = append(options, fmt.Sprint(e))
			}
			return fmt.Errorf("argument %q must be one of: %s", name, strings.Join(options, ", "))
		}
	}
	if s, ok := spec["pattern"].(string); ok && s != "" {
		re, err := regexp.Compile(s)
		if err != nil {
			return fmt.Errorf("invalid schema for %q", name)
		}
		str, _ := value.(string)
		if !re.MatchString(str) {
			return fmt.Errorf("argument %q does not match the required pattern", name)
		}
	}
	if n, ok := toFloat(spec["minimum"]); ok {
		if f, ok := toFloat(value); ok && f < n {
			return fmt.Errorf("argument %q must be >= %v", name, n)
		}
	}
	if n, ok := toFloat(spec["maximum"]); ok {
		if f, ok := toFloat(value); ok && f > n {
			return fmt.Errorf("argument %q must be <= %v", name, n)
		}
	}
	return nil
}

func checkType(name, want string, value any) error {
	ok := false
	switch want {
	case "string":
		_, ok = value.(string)
	case "number", "integer":
		_, ok = toFloat(value)
		if want == "integer" && ok {
			if f, _ := toFloat(value); f != math.Trunc(f) {
				ok = false
			}
		}
	case "boolean":
		_, ok = value.(bool)
	case "array":
		_, ok = value.([]any)
	case "object":
		_, ok = value.(map[string]any)
	default:
		return nil
	}
	if !ok {
		return fmt.Errorf("argument %q must be a %s, got %s", name, want, kindOf(value))
	}
	return nil
}

func kindOf(v any) string {
	switch v.(type) {
	case string:
		return "string"
	case bool:
		return "boolean"
	case float64, int, int64:
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	case nil:
		return "null"
	default:
		return reflect.TypeOf(v).String()
	}
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

func toStringList(v any) []string {
	items, ok := v.([]any)
	if !ok {
		if strs, ok := v.([]string); ok {
			return strs
		}
		return nil
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, fmt.Sprint(it))
	}
	return out
}
