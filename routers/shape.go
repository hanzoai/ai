// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package routers

import (
	"reflect"
	"strings"
	"time"
)

// The envelope told the truth and said nothing.
//
// Every operation here answers {status,msg,data,data2}, and that IS the shape —
// it is not a placeholder. But `data` carried a sentence where a type belongs
// ("The operation's own result."), so a reader who wanted the RESULT learned
// only that there was one. Two hundred operations shared that single sentence,
// which is why the fleet document counted them all as typed and every generated
// client handed back an untyped bag.
//
// The result's shape was never missing — it is the Go struct the handler already
// returns, with the json tags it already carries. So it is READ, not written:
// [shape] reflects a value into a schema, [components] collects the ones the
// resource table names, and the operation says which of them lands in `data`.
// Adding a field to object.Store changes the published contract in the same
// commit, because there is no second place to forget.

// shape is the OpenAPI schema of a Go value, read from its type.
//
// It follows json tags because those are what reaches the wire: a field tagged
// `json:"-"` is not in the response and must not be in the document, and an
// embedded struct's fields belong to the parent exactly as encoding/json flattens
// them. Cycles terminate at the $ref, since a named type refers to its component
// rather than expanding a second copy.
func shape(t reflect.Type, named map[string]reflect.Type) map[string]any {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	// time.Time marshals as an RFC 3339 string, not as its struct fields. Reading
	// the fields would publish wall/ext/loc, which no client has ever seen.
	if t == reflect.TypeOf(time.Time{}) {
		return map[string]any{"type": "string", "format": "date-time"}
	}

	switch t.Kind() {
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Slice, reflect.Array:
		// []byte marshals base64, as a string — not as an array of numbers.
		if t.Elem().Kind() == reflect.Uint8 {
			return map[string]any{"type": "string", "format": "byte"}
		}
		return map[string]any{"type": "array", "items": ref(t.Elem(), named)}
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": ref(t.Elem(), named)}
	case reflect.Struct:
		props := map[string]any{}
		fields(t, props, named)
		return map[string]any{"type": "object", "properties": props}
	}
	// Interfaces and anything else carry no declared shape. An empty schema says
	// "any JSON", which is the honest answer rather than a guessed one.
	return map[string]any{}
}

// fields writes t's exported fields into props, flattening embedded structs the
// way encoding/json does.
func fields(t reflect.Type, props map[string]any, named map[string]reflect.Type) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		if name == "-" {
			continue
		}
		if f.Anonymous && name == "" {
			e := f.Type
			for e.Kind() == reflect.Pointer {
				e = e.Elem()
			}
			if e.Kind() == reflect.Struct {
				fields(e, props, named)
				continue
			}
		}
		if name == "" {
			name = f.Name
		}
		props[name] = ref(f.Type, named)
	}
}

// ref is a $ref when the type has a name worth sharing, and the shape inline
// when it does not.
//
// Sharing matters for more than size: a client that sees object.Provider in two
// places should get ONE type, not two structurally-identical ones it cannot pass
// between. Anonymous and builtin types have no name to share, so they inline.
func ref(t reflect.Type, named map[string]reflect.Type) map[string]any {
	e := t
	for e.Kind() == reflect.Pointer {
		e = e.Elem()
	}
	if e.Kind() == reflect.Struct && e.Name() != "" && e != reflect.TypeOf(time.Time{}) {
		n := component(e)
		named[n] = e
		return map[string]any{"$ref": "#/components/schemas/" + n}
	}
	return shape(e, named)
}

// component is the document's name for a type: its Go name, qualified by the
// package it lives in when that package is not object.
//
// object is unqualified because it holds the nouns this service is about — a
// reader seeing "Store" in an ai document does not need to be told which Store.
func component(t reflect.Type) string {
	pkg := t.PkgPath()
	if i := strings.LastIndex(pkg, "/"); i >= 0 {
		pkg = pkg[i+1:]
	}
	if pkg == "" || pkg == "object" {
		return t.Name()
	}
	return pkg + "." + t.Name()
}

// components is every schema the document refers to, closed over its own
// references: reflecting Store names Provider, reflecting Provider names
// whatever Provider holds, and so on until nothing new appears.
func components(roots []reflect.Type) map[string]any {
	named := map[string]reflect.Type{}
	for _, t := range roots {
		named[component(t)] = t
	}
	out := map[string]any{}
	for len(out) != len(named) {
		for n, t := range named {
			if _, done := out[n]; done {
				continue
			}
			// shape may add to named; the loop runs again until it stops growing.
			out[n] = shape(t, named)
			break
		}
	}
	return out
}

// result is the envelope with `data` narrowed to what THIS operation returns.
//
// It is allOf rather than a rewritten envelope so the two facts stay separate:
// the envelope is the surface's contract and lives in one place, and the data
// shape is the operation's own. A generator that understands allOf gets both; one
// that does not still sees the envelope.
func result(data map[string]any) map[string]any {
	return map[string]any{
		"allOf": []any{
			map[string]any{"$ref": "#/components/schemas/Envelope"},
			map[string]any{
				"type":       "object",
				"properties": map[string]any{"data": data},
			},
		},
	}
}

// listOf and oneOf are what a collection and a member put in `data`.
func listOf(t reflect.Type) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"$ref": "#/components/schemas/" + component(t)}}
}

func oneOf(t reflect.Type) map[string]any {
	return map[string]any{"$ref": "#/components/schemas/" + component(t)}
}

// operationID is the name a client calls this operation by, and it must be the
// SAME name every other app in the fleet would give the same address.
//
// hanzoai/cloud composes this document with ~180 others and refuses duplicates,
// so an operation with no id is not merely unnamed — it collides with every
// other unnamed one and takes the whole composition down. Cloud derives ids for
// the apps it routes with zip.ID(method, path); this reproduces that rule rather
// than inventing a second one, because two spellings of one operation's name is
// exactly the drift this document exists to prevent.
func operationID(method, path string) string {
	var b strings.Builder
	b.WriteString(strings.ToLower(method))
	for _, seg := range strings.Split(path, "/") {
		if seg == "" {
			continue
		}
		b.WriteByte('_')
		if strings.HasPrefix(seg, "{") {
			b.WriteString("by_")
			seg = strings.Trim(seg, "{}")
		}
		b.WriteString(sanitizeID(seg))
	}
	return b.String()
}

// sanitizeID reduces a segment to the characters legal in an operationId and
// distinguishable from the '_' that separates segments.
func sanitizeID(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}
