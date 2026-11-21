package noisemig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// A parsed JSON value is one of: *object, []any, string, json.Number, bool, nil.

// member is one key/value pair of an object, in source order.
type member struct {
	key string
	val any
}

// object is a JSON object that keeps its keys in source order.
type object struct {
	members []member
}

func (o *object) index(key string) int {
	for i := len(o.members) - 1; i >= 0; i-- { // last wins, as in encoding/json
		if o.members[i].key == key {
			return i
		}
	}
	return -1
}

func (o *object) get(key string) (any, bool) {
	if i := o.index(key); i >= 0 {
		return o.members[i].val, true
	}
	return nil, false
}

// set replaces the value in place, or appends the key at the end.
func (o *object) set(key string, val any) {
	if i := o.index(key); i >= 0 {
		o.members[i].val = val
		return
	}
	o.members = append(o.members, member{key, val})
}

// del removes every occurrence of key.
func (o *object) del(key string) {
	kept := o.members[:0]
	for _, m := range o.members {
		if m.key != key {
			kept = append(kept, m)
		}
	}
	o.members = kept
}

func parseOrdered(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := decodeValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("json: trailing data after top-level value")
	}
	return v, nil
}

func decodeValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		if err == io.EOF {
			return nil, io.ErrUnexpectedEOF
		}
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			o := &object{}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, ok := kt.(string)
				if !ok {
					return nil, fmt.Errorf("json: object key is %T", kt)
				}
				v, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				o.members = append(o.members, member{key, v})
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
		return nil, fmt.Errorf("json: unexpected delimiter %q", t)
	default:
		return t, nil
	}
}

// encodeOrdered writes v with two-space indentation (the layout of
// json.MarshalIndent), no HTML escaping, and a trailing newline.
func encodeOrdered(v any) ([]byte, error) {
	var buf bytes.Buffer
	if err := encodeValue(&buf, v, 0); err != nil {
		return nil, err
	}
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}

func indent(buf *bytes.Buffer, depth int) {
	buf.WriteByte('\n')
	for i := 0; i < depth; i++ {
		buf.WriteString("  ")
	}
}

func encodeString(buf *bytes.Buffer, s string) error {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return err
	}
	buf.Write(bytes.TrimSuffix(b.Bytes(), []byte("\n")))
	return nil
}

func encodeValue(buf *bytes.Buffer, v any, depth int) error {
	switch t := v.(type) {
	case *object:
		if len(t.members) == 0 {
			buf.WriteString("{}")
			return nil
		}
		buf.WriteByte('{')
		for i, m := range t.members {
			if i > 0 {
				buf.WriteByte(',')
			}
			indent(buf, depth+1)
			if err := encodeString(buf, m.key); err != nil {
				return err
			}
			buf.WriteString(": ")
			if err := encodeValue(buf, m.val, depth+1); err != nil {
				return err
			}
		}
		indent(buf, depth)
		buf.WriteByte('}')
	case []any:
		if len(t) == 0 {
			buf.WriteString("[]")
			return nil
		}
		buf.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				buf.WriteByte(',')
			}
			indent(buf, depth+1)
			if err := encodeValue(buf, e, depth+1); err != nil {
				return err
			}
		}
		indent(buf, depth)
		buf.WriteByte(']')
	case string:
		return encodeString(buf, t)
	case json.Number:
		buf.WriteString(t.String())
	case bool:
		if t {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case nil:
		buf.WriteString("null")
	case int:
		fmt.Fprintf(buf, "%d", t)
	default:
		return fmt.Errorf("json: cannot encode %T", v)
	}
	return nil
}
