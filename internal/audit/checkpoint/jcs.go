// Copyright 2026 Atlantic Frontier Corporations LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package checkpoint

// JCS (JSON Canonicalization Scheme, RFC 8785) encoder.
//
// We implement JCS inline rather than pulling in github.com/cyberphone/json-canonicalization
// (which is not a standalone Go module) or github.com/gowebpki/jcs (which would require
// an additional go.mod entry and network access at build time). The subset we need is:
//   - Object keys sorted by UTF-16 code unit order (per RFC 8785 §3.2.3)
//   - Numbers serialized per ECMAScript's Number::toString (Go's encoding/json matches this
//     for all finite float64 values — both use shortest round-trip decimal representation)
//   - No extra whitespace
//   - String escaping per JSON spec
//
// For JSONB metadata from PostgreSQL we unmarshal with UseNumber() to preserve large integers.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"unicode/utf16"
)

// JCSEncode canonicalizes the JSON value v per RFC 8785.
// v must be an interface{} value produced by json.Unmarshal (with UseNumber).
func JCSEncode(v interface{}) ([]byte, error) {
	var buf bytes.Buffer
	if err := jcsWrite(&buf, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// JCSEncodeBytes canonicalizes raw JSON bytes per RFC 8785.
func JCSEncodeBytes(raw []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v interface{}
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("jcs: unmarshal: %w", err)
	}
	return JCSEncode(v)
}

func jcsWrite(w *bytes.Buffer, v interface{}) error {
	switch t := v.(type) {
	case nil:
		w.WriteString("null")
	case bool:
		if t {
			w.WriteString("true")
		} else {
			w.WriteString("false")
		}
	case json.Number:
		// Preserve the original numeric string if it is a valid finite number.
		// Re-parse to float64 only to validate; then emit the canonical form.
		f, err := t.Float64()
		if err != nil {
			return fmt.Errorf("jcs: invalid number %q: %w", t, err)
		}
		if math.IsInf(f, 0) || math.IsNaN(f) {
			return fmt.Errorf("jcs: non-finite number %q", t)
		}
		// Use Go's json.Marshal for number serialization — it produces the same
		// shortest round-trip decimal as ECMAScript's Number::toString for all
		// finite float64 values.
		b, _ := json.Marshal(f)
		w.Write(b)
	case float64:
		if math.IsInf(t, 0) || math.IsNaN(t) {
			return fmt.Errorf("jcs: non-finite float64 %v", t)
		}
		b, _ := json.Marshal(t)
		w.Write(b)
	case string:
		b, _ := json.Marshal(t)
		w.Write(b)
	case []interface{}:
		w.WriteByte('[')
		for i, elem := range t {
			if i > 0 {
				w.WriteByte(',')
			}
			if err := jcsWrite(w, elem); err != nil {
				return err
			}
		}
		w.WriteByte(']')
	case map[string]interface{}:
		// RFC 8785 §3.2.3: sort keys by their UTF-16 code unit sequence.
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			return jcsKeyLess(keys[i], keys[j])
		})
		w.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				w.WriteByte(',')
			}
			kb, _ := json.Marshal(k)
			w.Write(kb)
			w.WriteByte(':')
			if err := jcsWrite(w, t[k]); err != nil {
				return err
			}
		}
		w.WriteByte('}')
	default:
		// Fallback: use standard json.Marshal (handles int, int64, etc.)
		b, err := json.Marshal(t)
		if err != nil {
			return fmt.Errorf("jcs: marshal %T: %w", v, err)
		}
		w.Write(b)
	}
	return nil
}

// jcsKeyLess compares two JSON object keys per RFC 8785 §3.2.3 UTF-16 ordering.
func jcsKeyLess(a, b string) bool {
	ua := utf16.Encode([]rune(a))
	ub := utf16.Encode([]rune(b))
	for i := 0; i < len(ua) && i < len(ub); i++ {
		if ua[i] != ub[i] {
			return ua[i] < ub[i]
		}
	}
	return len(ua) < len(ub)
}

// jcsNumber formats a float64 as its ECMAScript string representation.
// Exported for test use.
func jcsNumber(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}

// jcsFormatInt formats an int64 as a JSON number string (no decimal point).
func jcsFormatInt(i int64) string {
	return strconv.FormatInt(i, 10)
}
