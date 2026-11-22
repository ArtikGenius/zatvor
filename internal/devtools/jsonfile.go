package devtools

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/tailscale/hujson"
)

// JSON and JSONC settings are edited as text at the offsets the hujson
// parser reports, never re-serialised: every byte outside the members we
// add, replace or remove (comments, other keys, blank lines, CRLF, a BOM)
// stays as it was. A member we add and later remove leaves the file
// byte-equal when nothing else was edited around it.

// jsonProjection is what the journal records for a JSON file: our keys as
// JSON pointers, each with its value or null when absent, and whether the
// file exists.
type jsonProjection struct {
	Keys   map[string]json.RawMessage `json:"keys"`
	Exists bool                       `json:"exists"`
}

var (
	errNotObject = errors.New("devtools: the settings file is not a JSON object")
	errDuplicate = errors.New("devtools: the settings file has one of our keys (or its parent) more than once")
)

// duplicated reports whether a name on the path to ptr occurs twice in its
// object: readers disagree on which one wins, so we do not edit such a file.
func duplicated(root *hujson.Value, ptr string) bool {
	v := root
	for _, seg := range strings.Split(ptr, "/")[1:] {
		obj, ok := v.Value.(*hujson.Object)
		if !ok {
			return false
		}
		name := strings.NewReplacer("~1", "/", "~0", "~").Replace(seg)
		first, n := -1, 0
		for i, m := range obj.Members {
			if lit, ok := m.Name.Value.(hujson.Literal); ok && lit.String() == name {
				if n++; first < 0 {
					first = i
				}
			}
		}
		if n > 1 {
			return true
		}
		if first < 0 {
			return false
		}
		v = &obj.Members[first].Value
	}
	return false
}

func isBlank(b []byte) bool { return len(bytes.TrimSpace(b)) == 0 }

func leadingBlank(b []byte) string {
	i := 0
	for i < len(b) && (b[i] == ' ' || b[i] == '\t') {
		i++
	}
	return string(b[:i])
}

func splice(b []byte, from, to int, s string) []byte {
	out := make([]byte, 0, len(b)-(to-from)+len(s))
	return append(append(append(out, b[:from]...), s...), b[to:]...)
}

// lineIndent returns the leading blanks of the line holding offset pos.
func lineIndent(b []byte, pos int) string {
	return leadingBlank(b[bytes.LastIndexByte(b[:pos], '\n')+1:])
}

func splitPointer(ptr string) (parent, name string) {
	i := strings.LastIndexByte(ptr, '/')
	return ptr[:i], strings.NewReplacer("~1", "/", "~0", "~").Replace(ptr[i+1:])
}

func parseObject(body []byte) (hujson.Value, error) {
	v, err := hujson.Parse(body)
	if err != nil {
		return v, err
	}
	if _, ok := v.Value.(*hujson.Object); !ok {
		return v, errNotObject
	}
	return v, nil
}

func memberIndex(obj *hujson.Object, name string) int {
	for i, m := range obj.Members {
		if lit, ok := m.Name.Value.(hujson.Literal); ok && lit.String() == name {
			return i
		}
	}
	return -1
}

// jsonGet returns the standard JSON of the value at ptr, or nil if absent.
func jsonGet(root *hujson.Value, ptr string) json.RawMessage {
	v := root.Find(ptr)
	if v == nil {
		return nil
	}
	c := v.Clone()
	c.Minimize()
	return json.RawMessage(c.Pack())
}

// jsonSet sets ptr to the JSON literal lit, creating its parent object if
// needed. unit is one indentation step, nl the file's newline.
func jsonSet(body []byte, ptr string, lit []byte, unit, nl string) ([]byte, error) {
	root, err := parseObject(body)
	if err != nil {
		return nil, err
	}
	if v := root.Find(ptr); v != nil {
		return splice(body, v.StartOffset, v.EndOffset, string(lit)), nil
	}
	parentPtr, name := splitPointer(ptr)
	pv := root.Find(parentPtr)
	if pv == nil {
		if body, err = jsonSet(body, parentPtr, []byte("{}"), unit, nl); err != nil {
			return nil, err
		}
		return jsonSet(body, ptr, lit, unit, nl)
	}
	obj, ok := pv.Value.(*hujson.Object)
	if !ok {
		return nil, fmt.Errorf("devtools: %s is not a JSON object", parentPtr)
	}
	key, _ := json.Marshal(name)
	member := string(key) + ": " + string(lit)
	if n := len(obj.Members); n > 0 {
		last := obj.Members[n-1]
		sep := " "
		if i := bytes.LastIndexByte(last.Name.BeforeExtra, '\n'); i >= 0 {
			sep = nl + leadingBlank(last.Name.BeforeExtra[i+1:])
		}
		// Right after the last value: a trailing comma, if any, now
		// follows our member.
		return splice(body, last.Value.EndOffset, last.Value.EndOffset, ","+sep+member), nil
	}
	open := pv.StartOffset
	indent := lineIndent(body, open)
	ins := nl + indent + unit + member
	if !bytes.Contains(body[open+1:pv.EndOffset-1], []byte("\n")) {
		ins += nl + indent
	}
	return splice(body, open+1, open+1, ins), nil
}

// jsonRemove removes the member at ptr, if present, and then its parent
// object if that is left empty (we created it, or it held only our keys).
func jsonRemove(body []byte, ptr string) ([]byte, error) {
	root, err := parseObject(body)
	if err != nil {
		return nil, err
	}
	parentPtr, name := splitPointer(ptr)
	pv := root.Find(parentPtr)
	if pv == nil {
		return body, nil
	}
	obj, ok := pv.Value.(*hujson.Object)
	if !ok {
		return body, nil
	}
	idx := memberIndex(obj, name)
	if idx < 0 {
		return body, nil
	}
	body = removeMember(body, obj, idx)
	if parentPtr == "" {
		return body, nil
	}
	root, err = parseObject(body)
	if err != nil {
		return nil, err
	}
	if pv = root.Find(parentPtr); pv != nil {
		if o, ok := pv.Value.(*hujson.Object); ok && len(o.Members) == 0 && isBlank(body[pv.StartOffset+1:pv.EndOffset-1]) {
			return jsonRemove(body, parentPtr)
		}
	}
	return body, nil
}

func removeMember(body []byte, obj *hujson.Object, idx int) []byte {
	ms := obj.Members
	n := len(ms)
	m := ms[idx]
	commaAfter := func(i int) int { return ms[i].Value.EndOffset + len(ms[i].Value.AfterExtra) }
	trailing := idx == n-1 && m.Value.AfterExtra != nil // a trailing comma follows the last member
	if idx > 0 {
		// The form jsonSet adds: `,` + blanks + member right after the
		// previous value. Removing exactly that restores the old bytes.
		if c := commaAfter(idx - 1); isBlank(body[c+1 : m.Name.StartOffset]) {
			return splice(body, c, m.Value.EndOffset, "")
		}
	}
	// Otherwise cut the member with its own comma and keep the comments
	// around it; a line left blank goes too.
	a, b := m.Name.StartOffset, m.Value.EndOffset
	if idx < n-1 || trailing {
		b = commaAfter(idx) + 1
	}
	out := cutLine(body, a, b)
	if idx > 0 && idx == n-1 && !trailing {
		c := commaAfter(idx - 1) // before a, so unaffected by the cut
		out = splice(out, c, c+1, "")
	}
	return out
}

// cutLine deletes b[from:to], and the whole line when nothing but blanks
// would be left on it.
func cutLine(b []byte, from, to int) []byte {
	s := from
	for s > 0 && (b[s-1] == ' ' || b[s-1] == '\t') {
		s--
	}
	e := to
	for e < len(b) && (b[e] == ' ' || b[e] == '\t') {
		e++
	}
	if e < len(b) && b[e] == '\r' && e+1 < len(b) && b[e+1] == '\n' {
		e += 2
	} else if e < len(b) && b[e] == '\n' {
		e++
	} else {
		return splice(b, from, to, "")
	}
	if s > 0 && b[s-1] != '\n' {
		return splice(b, from, to, "")
	}
	return splice(b, s, e, "")
}

// jsonDoc is a settings file split into its BOM and body.
type jsonDoc struct {
	bom  []byte
	body []byte
	nl   string
	unit string
}

func newJSONDoc(data []byte, defUnit string) jsonDoc {
	d := jsonDoc{body: data, nl: "\n", unit: defUnit}
	if hasPrefix(data, bomUTF8) {
		d.bom, d.body = bomUTF8, data[3:]
	}
	if bytes.Contains(d.body, []byte("\r\n")) {
		d.nl = "\r\n"
	}
	if isBlank(d.body) {
		d.body = []byte("{}" + d.nl)
	}
	if root, err := parseObject(d.body); err == nil {
		if o := root.Value.(*hujson.Object); len(o.Members) > 0 {
			be := o.Members[0].Name.BeforeExtra
			if i := bytes.LastIndexByte(be, '\n'); i >= 0 {
				if u := leadingBlank(be[i+1:]); u != "" {
					d.unit = u
				}
			}
		}
	}
	return d
}

func (d jsonDoc) bytes() []byte { return append(append([]byte(nil), d.bom...), d.body...) }

func (d jsonDoc) projection(ptrs []string, exists bool) (json.RawMessage, error) {
	root, err := parseObject(d.body)
	if err != nil {
		return nil, err
	}
	p := jsonProjection{Keys: map[string]json.RawMessage{}, Exists: exists}
	for _, ptr := range ptrs {
		if duplicated(&root, ptr) {
			return nil, errDuplicate
		}
		v := jsonGet(&root, ptr)
		if v == nil {
			v = json.RawMessage("null")
		}
		p.Keys[ptr] = v
	}
	return json.Marshal(p)
}

// set applies vals (ptr → JSON literal, or nil to remove) in the order of
// ptrs; removals run in reverse order so nested members go before their
// parents. The result must still parse, and stay standard JSON if it was.
func (d jsonDoc) set(ptrs []string, vals map[string]json.RawMessage) (jsonDoc, error) {
	orig, err := parseObject(d.body)
	if err != nil {
		return d, err
	}
	body := d.body
	for _, ptr := range ptrs {
		if v := vals[ptr]; v != nil && string(v) != "null" {
			if body, err = jsonSet(body, ptr, v, d.unit, d.nl); err != nil {
				return d, err
			}
		}
	}
	for i := len(ptrs) - 1; i >= 0; i-- {
		if v, ok := vals[ptrs[i]]; ok && (v == nil || string(v) == "null") {
			if body, err = jsonRemove(body, ptrs[i]); err != nil {
				return d, err
			}
		}
	}
	res, err := parseObject(body)
	if err != nil {
		return d, fmt.Errorf("devtools: edit produced invalid JSON: %w", err)
	}
	if orig.IsStandard() && !res.IsStandard() {
		return d, errors.New("devtools: edit would make strict JSON non-standard")
	}
	d.body = body
	return d, nil
}
