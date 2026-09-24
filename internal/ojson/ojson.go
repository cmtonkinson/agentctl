// Package ojson edits JSON objects while preserving key order, so config
// files owned by other applications keep their layout when agentctl adds or
// removes an entry.
package ojson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Object is a JSON object whose keys keep their original order.
type Object struct {
	keys []string
	vals map[string]json.RawMessage
}

// New returns an empty object.
func New() *Object { return &Object{vals: map[string]json.RawMessage{}} }

// Parse decodes a JSON object, keeping key order. Empty input yields an
// empty object.
func Parse(data []byte) (*Object, error) {
	o := New()
	if len(bytes.TrimSpace(data)) == 0 {
		return o, nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	t, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := t.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("expected a JSON object")
	}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := t.(string)
		if !ok {
			return nil, fmt.Errorf("expected an object key")
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		o.Set(key, raw)
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	return o, nil
}

// Keys returns the keys in order.
func (o *Object) Keys() []string { return append([]string(nil), o.keys...) }

// Get returns the raw value for key.
func (o *Object) Get(key string) (json.RawMessage, bool) {
	v, ok := o.vals[key]
	return v, ok
}

// Set replaces key's value in place, or appends the key.
func (o *Object) Set(key string, v json.RawMessage) {
	if _, ok := o.vals[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = v
}

// Delete removes key.
func (o *Object) Delete(key string) {
	if _, ok := o.vals[key]; !ok {
		return
	}
	delete(o.vals, key)
	for i, k := range o.keys {
		if k == key {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

// MarshalJSON encodes the object compactly in key order.
func (o *Object) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		b.Write(kb)
		b.WriteByte(':')
		var c bytes.Buffer
		if err := json.Compact(&c, o.vals[k]); err != nil {
			return nil, err
		}
		b.Write(c.Bytes())
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// Format encodes the object with the given indent and a trailing newline.
func (o *Object) Format(indent string) ([]byte, error) {
	compact, err := o.MarshalJSON()
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, compact, "", indent); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

// DetectIndent guesses the indentation used by an existing JSON document.
func DetectIndent(data []byte) string {
	for _, line := range strings.Split(string(data), "\n")[1:] {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed == "" || len(trimmed) == len(line) {
			continue
		}
		return line[:len(line)-len(trimmed)]
	}
	return "  "
}
