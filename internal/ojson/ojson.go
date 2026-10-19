// Package ojson decodes and encodes JSON while keeping object keys in their
// original order and numbers in their original text, so a repair that touches
// one field leaves everything else looking the way the upstream wrote it.
//
// Values are *Object, []any, string, json.Number, bool or nil.
package ojson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Object is a JSON object with ordered keys.
type Object struct {
	keys []string
	vals map[string]any
}

// NewObject returns an empty object.
func NewObject() *Object { return &Object{vals: map[string]any{}} }

// Keys returns the keys in order.
func (o *Object) Keys() []string { return append([]string(nil), o.keys...) }

// Get returns the value for key.
func (o *Object) Get(key string) (any, bool) {
	v, ok := o.vals[key]
	return v, ok
}

// Set sets key, keeping its position if it already exists.
func (o *Object) Set(key string, v any) {
	if _, ok := o.vals[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = v
}

// Delete removes key and reports whether it was present.
func (o *Object) Delete(key string) bool {
	if _, ok := o.vals[key]; !ok {
		return false
	}
	delete(o.vals, key)
	for i, k := range o.keys {
		if k == key {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
	return true
}

// Decode parses one JSON value. It rejects trailing data, and anything that
// would not survive a round trip unchanged: invalid UTF-8, unpaired UTF-16
// surrogate escapes and duplicate object keys.
func Decode(data []byte) (any, error) {
	if !utf8.Valid(data) {
		return nil, errors.New("invalid UTF-8")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := decodeValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("unexpected data after the JSON value")
	}
	// encoding/json turns an unpaired surrogate escape into U+FFFD. Count
	// replacement characters before and after to catch that.
	enc, err := Encode(v)
	if err != nil {
		return nil, err
	}
	before := bytes.Count(data, []byte("\uFFFD")) + bytes.Count(bytes.ToLower(data), []byte(`\ufffd`))
	if bytes.Count(enc, []byte("\uFFFD")) > before {
		return nil, errors.New("unpaired UTF-16 surrogate escape")
	}
	return v, nil
}

func decodeValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		if err == io.EOF {
			return nil, errors.New("empty JSON body")
		}
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			o := NewObject()
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, ok := kt.(string)
				if !ok {
					return nil, fmt.Errorf("object key is not a string")
				}
				v, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				if _, dup := o.Get(key); dup {
					return nil, fmt.Errorf("duplicate key %q", key)
				}
				o.Set(key, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return o, nil
		case '[':
			arr := []any{}
			for dec.More() {
				v, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return arr, nil
		}
		return nil, fmt.Errorf("unexpected %v", t)
	default:
		return t, nil
	}
}

// Encode writes v as compact JSON.
func Encode(v any) ([]byte, error) {
	var b bytes.Buffer
	if err := encode(&b, v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func encode(b *bytes.Buffer, v any) error {
	switch t := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if t {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case json.Number:
		if !json.Valid([]byte(t)) {
			return fmt.Errorf("invalid number %q", string(t))
		}
		b.WriteString(string(t))
	case string:
		writeString(b, t)
	case []any:
		b.WriteByte('[')
		for i, it := range t {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := encode(b, it); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case *Object:
		b.WriteByte('{')
		for i, k := range t.keys {
			if i > 0 {
				b.WriteByte(',')
			}
			writeString(b, k)
			b.WriteByte(':')
			if err := encode(b, t.vals[k]); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	default:
		return fmt.Errorf("unsupported JSON value of type %T", v)
	}
	return nil
}

func writeString(b *bytes.Buffer, s string) {
	var tmp bytes.Buffer
	enc := json.NewEncoder(&tmp)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s) // strings always encode
	b.Write(bytes.TrimSuffix(tmp.Bytes(), []byte("\n")))
}

// Clone returns a deep copy of v.
func Clone(v any) any {
	switch t := v.(type) {
	case *Object:
		o := NewObject()
		for _, k := range t.keys {
			o.Set(k, Clone(t.vals[k]))
		}
		return o
	case []any:
		out := make([]any, len(t))
		for i, it := range t {
			out[i] = Clone(it)
		}
		return out
	}
	return v
}

// Equal reports whether a and b encode to the same JSON.
func Equal(a, b any) bool {
	ea, err1 := Encode(a)
	eb, err2 := Encode(b)
	return err1 == nil && err2 == nil && bytes.Equal(ea, eb)
}

// Path is a parsed dot path such as "customer.addresses.0.city".
type Path []string

func (p Path) String() string { return strings.Join(p, ".") }

// ParsePath splits a dot path. Numeric segments index arrays.
func ParsePath(s string) (Path, error) {
	if s == "" {
		return nil, errors.New("empty path")
	}
	parts := strings.Split(s, ".")
	for _, p := range parts {
		if p == "" {
			return nil, fmt.Errorf("path %q has an empty segment", s)
		}
	}
	return Path(parts), nil
}

func index(seg string, n int) (int, bool) {
	i, err := strconv.Atoi(seg)
	if err != nil || i < 0 || i >= n || strconv.Itoa(i) != seg {
		return 0, false
	}
	return i, true
}

// Get returns the value at path.
func Get(root any, p Path) (any, bool) {
	cur := root
	for _, seg := range p {
		switch c := cur.(type) {
		case *Object:
			v, ok := c.Get(seg)
			if !ok {
				return nil, false
			}
			cur = v
		case []any:
			i, ok := index(seg, len(c))
			if !ok {
				return nil, false
			}
			cur = c[i]
		default:
			return nil, false
		}
	}
	return cur, true
}

// ErrPath is returned when a path runs through a value that is not an
// object or array.
var ErrPath = errors.New("path goes through a value that is not an object or array")

// Set stores v at path, creating missing objects along the way. It reports
// whether the document changed.
func Set(root any, p Path, v any) (bool, error) {
	if len(p) == 0 {
		return false, errors.New("empty path")
	}
	parent, err := walkCreate(root, p[:len(p)-1])
	if err != nil {
		return false, err
	}
	last := p[len(p)-1]
	switch c := parent.(type) {
	case *Object:
		if old, ok := c.Get(last); ok && Equal(old, v) {
			return false, nil
		}
		c.Set(last, v)
		return true, nil
	case []any:
		i, ok := index(last, len(c))
		if !ok {
			return false, fmt.Errorf("array index %q out of range", last)
		}
		if Equal(c[i], v) {
			return false, nil
		}
		c[i] = v
		return true, nil
	}
	return false, ErrPath
}

func walkCreate(root any, p Path) (any, error) {
	cur := root
	for _, seg := range p {
		switch c := cur.(type) {
		case *Object:
			v, ok := c.Get(seg)
			if !ok || v == nil {
				child := NewObject()
				c.Set(seg, child)
				v = child
			}
			cur = v
		case []any:
			i, ok := index(seg, len(c))
			if !ok {
				return nil, fmt.Errorf("array index %q out of range", seg)
			}
			cur = c[i]
		default:
			return nil, ErrPath
		}
	}
	switch cur.(type) {
	case *Object, []any:
		return cur, nil
	}
	return nil, ErrPath
}

// Rename moves the value at from to to. When both live in the same object
// and to does not exist yet, the key keeps its position. Otherwise the value
// is set at to (replacing what was there) and removed from from.
func Rename(root any, from, to Path) (bool, error) {
	v, ok := Get(root, from)
	if !ok {
		return false, fmt.Errorf("%s not found", from)
	}
	if len(from) == len(to) && Path(from[:len(from)-1]).String() == Path(to[:len(to)-1]).String() {
		parent, _ := Get(root, from[:len(from)-1])
		if o, ok := parent.(*Object); ok {
			newKey := to[len(to)-1]
			if _, exists := o.Get(newKey); !exists {
				oldKey := from[len(from)-1]
				for i, k := range o.keys {
					if k == oldKey {
						o.keys[i] = newKey
						break
					}
				}
				delete(o.vals, oldKey)
				o.vals[newKey] = v
				return true, nil
			}
		}
	}
	if _, err := Set(root, to, Clone(v)); err != nil {
		return false, err
	}
	Delete(root, from)
	return true, nil
}

// Delete removes the value at path and reports whether it existed. Array
// elements are removed and the rest shift down.
func Delete(root any, p Path) bool {
	if len(p) == 0 {
		return false
	}
	parentPath, last := p[:len(p)-1], p[len(p)-1]
	parent, ok := Get(root, parentPath)
	if !ok {
		return false
	}
	switch c := parent.(type) {
	case *Object:
		return c.Delete(last)
	case []any:
		i, ok := index(last, len(c))
		if !ok {
			return false
		}
		newArr := append(append([]any{}, c[:i]...), c[i+1:]...)
		if len(parentPath) == 0 {
			return false // cannot shrink a root array in place
		}
		gp, _ := Get(root, parentPath[:len(parentPath)-1])
		switch g := gp.(type) {
		case *Object:
			g.Set(parentPath[len(parentPath)-1], newArr)
		case []any:
			j, _ := index(parentPath[len(parentPath)-1], len(g))
			g[j] = newArr
		}
		return true
	}
	return false
}
