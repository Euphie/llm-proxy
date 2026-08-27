package strategy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/Euphie/llm-proxy/internal/profile"
)

type Override struct {
	Path   string          `json:"path"`
	Value  json.RawMessage `json:"value,omitempty"`
	Remove bool            `json:"remove,omitempty"`
}

type OverridePatch []Override

func DiffOverrides(
	generated profile.RoutingStrategyConfig,
	edited profile.RoutingStrategyConfig,
) (OverridePatch, error) {
	before, err := jsonValue(generated)
	if err != nil {
		return nil, err
	}
	after, err := jsonValue(edited)
	if err != nil {
		return nil, err
	}
	patch := diffJSON(before, after, "")
	sort.Slice(patch, func(i, j int) bool { return patch[i].Path < patch[j].Path })
	return patch, nil
}

func ApplyOverrides(
	generated profile.RoutingStrategyConfig,
	patch OverridePatch,
) (profile.RoutingStrategyConfig, error) {
	value, err := jsonValue(generated)
	if err != nil {
		return profile.RoutingStrategyConfig{}, err
	}
	applied, err := applyJSON(value, patch)
	if err != nil {
		return profile.RoutingStrategyConfig{}, err
	}
	contents, err := json.Marshal(applied)
	if err != nil {
		return profile.RoutingStrategyConfig{}, fmt.Errorf("encode overridden strategy: %w", err)
	}
	var config profile.RoutingStrategyConfig
	if err := json.Unmarshal(contents, &config); err != nil {
		return profile.RoutingStrategyConfig{}, fmt.Errorf("decode overridden strategy: %w", err)
	}
	return config, nil
}

func RemoveOverride(patch OverridePatch, path string) OverridePatch {
	result := make(OverridePatch, 0, len(patch))
	for _, override := range patch {
		if override.Path == path || strings.HasPrefix(override.Path, strings.TrimSuffix(path, "/")+"/") {
			continue
		}
		result = append(result, override)
	}
	return result
}

func diffJSON(before, after any, path string) OverridePatch {
	if reflect.DeepEqual(before, after) {
		return nil
	}
	beforeObject, beforeIsObject := before.(map[string]any)
	afterObject, afterIsObject := after.(map[string]any)
	if beforeIsObject && afterIsObject {
		keys := make(map[string]struct{}, len(beforeObject)+len(afterObject))
		for key := range beforeObject {
			keys[key] = struct{}{}
		}
		for key := range afterObject {
			keys[key] = struct{}{}
		}
		ordered := make([]string, 0, len(keys))
		for key := range keys {
			ordered = append(ordered, key)
		}
		sort.Strings(ordered)
		var patch OverridePatch
		for _, key := range ordered {
			beforeValue, beforeFound := beforeObject[key]
			afterValue, afterFound := afterObject[key]
			childPath := path + "/" + escapePointerToken(key)
			switch {
			case beforeFound && !afterFound:
				patch = append(patch, Override{Path: childPath, Remove: true})
			case !beforeFound && afterFound:
				patch = append(patch, valueOverride(childPath, afterValue))
			default:
				patch = append(patch, diffJSON(beforeValue, afterValue, childPath)...)
			}
		}
		return patch
	}
	beforeArray, beforeIsArray := before.([]any)
	afterArray, afterIsArray := after.([]any)
	if beforeIsArray && afterIsArray && len(beforeArray) == len(afterArray) {
		var patch OverridePatch
		for index := range beforeArray {
			patch = append(patch, diffJSON(
				beforeArray[index], afterArray[index], path+"/"+strconv.Itoa(index),
			)...)
		}
		return patch
	}
	return OverridePatch{valueOverride(path, after)}
}

func valueOverride(path string, value any) Override {
	contents, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return Override{Path: path, Value: contents}
}

func applyJSON(value any, patch OverridePatch) (any, error) {
	cloned, err := jsonValue(value)
	if err != nil {
		return nil, err
	}
	for _, override := range patch {
		if err := validateOverride(override); err != nil {
			return nil, err
		}
		tokens, err := pointerTokens(override.Path)
		if err != nil {
			return nil, err
		}
		cloned, err = applyAt(cloned, tokens, override)
		if err != nil {
			return nil, fmt.Errorf("apply override %s: %w", override.Path, err)
		}
	}
	return cloned, nil
}

func applyAt(current any, tokens []string, override Override) (any, error) {
	if len(tokens) == 0 {
		if override.Remove {
			return nil, errors.New("cannot remove document root")
		}
		return decodeRawValue(override.Value)
	}
	switch container := current.(type) {
	case map[string]any:
		key := tokens[0]
		if len(tokens) == 1 {
			if override.Remove {
				delete(container, key)
				return container, nil
			}
			value, err := decodeRawValue(override.Value)
			if err != nil {
				return nil, err
			}
			container[key] = value
			return container, nil
		}
		child, found := container[key]
		if !found {
			return nil, fmt.Errorf("object key %q does not exist", key)
		}
		updated, err := applyAt(child, tokens[1:], override)
		if err != nil {
			return nil, err
		}
		container[key] = updated
		return container, nil
	case []any:
		index, err := strconv.Atoi(tokens[0])
		if err != nil || index < 0 || index >= len(container) {
			return nil, fmt.Errorf("array index %q is invalid", tokens[0])
		}
		if len(tokens) == 1 {
			if override.Remove {
				return append(container[:index], container[index+1:]...), nil
			}
			value, err := decodeRawValue(override.Value)
			if err != nil {
				return nil, err
			}
			container[index] = value
			return container, nil
		}
		updated, err := applyAt(container[index], tokens[1:], override)
		if err != nil {
			return nil, err
		}
		container[index] = updated
		return container, nil
	default:
		return nil, fmt.Errorf("cannot descend into %T", current)
	}
}

func validateOverride(override Override) error {
	if override.Path != "" && !strings.HasPrefix(override.Path, "/") {
		return fmt.Errorf("override path %q must be a JSON Pointer", override.Path)
	}
	if override.Remove && len(override.Value) > 0 {
		return fmt.Errorf("override %q cannot remove and set a value", override.Path)
	}
	if !override.Remove && len(override.Value) == 0 {
		return fmt.Errorf("override %q requires a value", override.Path)
	}
	return nil
}

func pointerTokens(path string) ([]string, error) {
	if path == "" {
		return nil, nil
	}
	if !strings.HasPrefix(path, "/") {
		return nil, fmt.Errorf("invalid JSON Pointer %q", path)
	}
	raw := strings.Split(path[1:], "/")
	for index, token := range raw {
		decoded := strings.ReplaceAll(token, "~1", "/")
		decoded = strings.ReplaceAll(decoded, "~0", "~")
		raw[index] = decoded
	}
	return raw, nil
}

func escapePointerToken(token string) string {
	token = strings.ReplaceAll(token, "~", "~0")
	return strings.ReplaceAll(token, "/", "~1")
}

func decodeRawValue(raw json.RawMessage) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("decode override value: %w", err)
	}
	return value, nil
}

func jsonValue(value any) (any, error) {
	contents, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode JSON value: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode JSON value: %w", err)
	}
	return decoded, nil
}
