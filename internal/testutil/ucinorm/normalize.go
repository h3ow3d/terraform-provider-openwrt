package ucinorm

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

type Snapshot struct {
	Sections []Section
}

type Section struct {
	ContainerKey string
	Name         string
	Type         string
	Anonymous    bool
	Index        int
	Options      map[string]any
}

func ParseDHCPPackageGetResponse(body []byte) (Snapshot, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return Snapshot{}, errors.New("invalid json-rpc response")
	}
	if _, ok := envelope["result"]; !ok {
		return Snapshot{}, errors.New("missing result envelope")
	}

	var result []json.RawMessage
	if err := json.Unmarshal(envelope["result"], &result); err != nil || len(result) < 2 {
		return Snapshot{}, errors.New("invalid result envelope")
	}
	var status int
	if err := json.Unmarshal(result[0], &status); err != nil {
		return Snapshot{}, errors.New("invalid ubus status type")
	}
	if status != 0 {
		return Snapshot{}, fmt.Errorf("non-zero ubus status: %d", status)
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(result[1], &payload); err != nil {
		return Snapshot{}, errors.New("invalid result payload")
	}
	rawValues, ok := payload["values"]
	if !ok {
		return Snapshot{}, errors.New("missing values payload")
	}

	var values map[string]json.RawMessage
	if err := json.Unmarshal(rawValues, &values); err != nil {
		return Snapshot{}, errors.New("values payload must be object")
	}

	keySeen := map[string]bool{}
	nameSeen := map[string]bool{}
	sections := make([]Section, 0, len(values))
	for key, rawSection := range values {
		if key == "" {
			return Snapshot{}, errors.New("empty container key")
		}
		if keySeen[key] {
			return Snapshot{}, errors.New("duplicate container key")
		}
		keySeen[key] = true
		section, err := parseSection(key, rawSection)
		if err != nil {
			return Snapshot{}, err
		}
		if nameSeen[section.Name] {
			return Snapshot{}, errors.New("ambiguous section identity")
		}
		nameSeen[section.Name] = true
		sections = append(sections, section)
	}

	slices.SortFunc(sections, func(a, b Section) int {
		if a.Index != b.Index {
			return a.Index - b.Index
		}
		if a.ContainerKey < b.ContainerKey {
			return -1
		}
		if a.ContainerKey > b.ContainerKey {
			return 1
		}
		return 0
	})
	return Snapshot{Sections: sections}, nil
}

func (s Snapshot) CanonicalJSON() ([]byte, error) {
	normalized := make([]map[string]any, 0, len(s.Sections))
	for _, sec := range s.Sections {
		optionKeys := make([]string, 0, len(sec.Options))
		for key := range sec.Options {
			optionKeys = append(optionKeys, key)
		}
		slices.Sort(optionKeys)
		opts := map[string]any{}
		for _, key := range optionKeys {
			opts[key] = sec.Options[key]
		}
		normalized = append(normalized, map[string]any{
			"container_key": sec.ContainerKey,
			"name":          sec.Name,
			"type":          sec.Type,
			"anonymous":     sec.Anonymous,
			"index":         sec.Index,
			"options":       opts,
		})
	}
	return json.Marshal(normalized)
}

func parseSection(key string, rawSection json.RawMessage) (Section, error) {
	var section map[string]json.RawMessage
	if err := json.Unmarshal(rawSection, &section); err != nil {
		return Section{}, errors.New("malformed section object")
	}

	name, ok := readString(section, ".name")
	if !ok || name == "" {
		return Section{}, errors.New("invalid section metadata")
	}
	typ, ok := readString(section, ".type")
	if !ok || typ == "" {
		return Section{}, errors.New("invalid section metadata")
	}
	anonymous, ok := readBool(section, ".anonymous")
	if !ok {
		return Section{}, errors.New("invalid section metadata")
	}
	index, ok := readInt(section, ".index")
	if !ok || index < 0 {
		return Section{}, errors.New("invalid section metadata")
	}

	options := map[string]any{}
	for field, value := range section {
		if field == ".name" || field == ".type" || field == ".anonymous" || field == ".index" {
			continue
		}
		decoded, err := decodeOptionValue(value)
		if err != nil {
			return Section{}, errors.New("invalid option shape")
		}
		options[field] = decoded
	}

	return Section{
		ContainerKey: key,
		Name:         name,
		Type:         typ,
		Anonymous:    anonymous,
		Index:        index,
		Options:      options,
	}, nil
}

func decodeOptionValue(raw json.RawMessage) (any, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	switch typed := value.(type) {
	case string, float64, bool:
		return typed, nil
	case []any:
		for _, item := range typed {
			switch item.(type) {
			case string, float64, bool:
			default:
				return nil, errors.New("array option item must be scalar")
			}
		}
		return typed, nil
	default:
		return nil, errors.New("unsupported option value type")
	}
}

func readString(raw map[string]json.RawMessage, key string) (string, bool) {
	value, ok := raw[key]
	if !ok {
		return "", false
	}
	var out string
	if err := json.Unmarshal(value, &out); err != nil {
		return "", false
	}
	return out, true
}

func readBool(raw map[string]json.RawMessage, key string) (bool, bool) {
	value, ok := raw[key]
	if !ok {
		return false, false
	}
	var out bool
	if err := json.Unmarshal(value, &out); err != nil {
		return false, false
	}
	return out, true
}

func readInt(raw map[string]json.RawMessage, key string) (int, bool) {
	value, ok := raw[key]
	if !ok {
		return 0, false
	}
	var out int
	if err := json.Unmarshal(value, &out); err != nil {
		return 0, false
	}
	return out, true
}
