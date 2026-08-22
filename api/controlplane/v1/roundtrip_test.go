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

// This file holds the protocol conformance tests for controlplanev1.
//
// The gateway has no JSON Schema validation library and this package
// deliberately adds no dependency, so full schema validation of instance
// documents lives in the control plane, which does have one. What is enforced
// here instead is the property that would otherwise let the two drift apart
// unnoticed: that the Go types and the schema documents describe the same
// message. A field added to one and not the other fails here, in the repo that
// owns the contract.
package controlplanev1

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// sampleSubmission is a checkpoint as a current gateway would send it: the
// reserved fields are absent, because nothing in the gateway computes them.
func sampleSubmission() CheckpointSubmission {
	prev := int64(41)
	return CheckpointSubmission{
		GatewayID:            "9f8d3a1e-4c2b-4f5a-8e7d-1b2c3d4e5f60",
		CheckpointID:         42,
		RangeStart:           10001,
		RangeEnd:             20000,
		EventCount:           10000,
		MerkleRoot:           HashHex(strings.Repeat("a3", 32)),
		PrevCheckpointID:     &prev,
		PrevCheckpointHash:   HashHex(strings.Repeat("b7", 32)),
		CheckpointHash:       HashHex(strings.Repeat("c1", 32)),
		HashAlgorithm:        HashAlgorithmSHA256,
		HashSchemaVersion:    1,
		CanonicalizationSpec: CanonicalizationRFC8785V1,
		SealedAt:             NewTimestamp(time.Date(2026, 8, 21, 14, 0, 0, 123456000, time.UTC)),
		SealerVersion:        "1.2.3",
		GatewayVersion:       "1.2.3",
		CoveredFrom:          NewTimestamp(time.Date(2026, 8, 21, 13, 0, 0, 0, time.UTC)),
		CoveredTo:            NewTimestamp(time.Date(2026, 8, 21, 13, 59, 59, 999999000, time.UTC)),
	}
}

// sampleSubmissionWithReserved populates the reserved fields, which a v1
// emitter may not do. It exists so the tests can assert that populating them is
// rejected rather than quietly accepted.
func sampleSubmissionWithReserved() CheckpointSubmission {
	s := sampleSubmission()
	s.ConfigHash = HashHex(strings.Repeat("d2", 32))
	s.PolicyBundles = []PolicyBundleRef{{
		Name:          "default",
		Digest:        HashHex(strings.Repeat("e4", 32)),
		HashAlgorithm: HashAlgorithmSHA256,
	}}
	return s
}

func TestRoundTrip_CheckpointSubmission(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		in   CheckpointSubmission
	}{
		{"as a current gateway sends it", sampleSubmission()},
		{"genesis", func() CheckpointSubmission {
			s := sampleSubmission()
			s.CheckpointID = 1
			s.RangeStart, s.RangeEnd, s.EventCount = 1, 10000, 10000
			s.PrevCheckpointID = nil
			s.PrevCheckpointHash = GenesisPrevHash
			return s
		}()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if err := tc.in.Validate(); err != nil {
				t.Fatalf("the sample is not valid: %v", err)
			}

			encoded, err := json.Marshal(tc.in)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			assertSchemaShape(t, SchemaCheckpointSubmission, encoded)

			var out CheckpointSubmission
			dec := json.NewDecoder(strings.NewReader(string(encoded)))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&out); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if err := out.Validate(); err != nil {
				t.Fatalf("the decoded value is not valid: %v", err)
			}
			if !reflect.DeepEqual(tc.in, out) {
				t.Errorf("value changed across the round trip\n before: %+v\n  after: %+v", tc.in, out)
			}
		})
	}
}

func TestRoundTrip_GatewayRegistration(t *testing.T) {
	t.Parallel()

	in := GatewayRegistration{Name: "eu-west-prod", GatewayVersion: "1.2.3"}
	if err := in.Validate(); err != nil {
		t.Fatalf("the sample is not valid: %v", err)
	}

	encoded, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	assertSchemaShape(t, SchemaGatewayRegistration, encoded)

	var out GatewayRegistration
	if err := json.Unmarshal(encoded, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if in != out {
		t.Errorf("value changed across the round trip: %+v then %+v", in, out)
	}
}

// TestSchemaMatchesGoType is the drift guard. Without a schema validator, the
// failure mode this package has to prevent is a field that exists on one side
// of the contract and not the other, which no round trip would notice.
func TestSchemaMatchesGoType(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		schemaPath string
		goType     reflect.Type
	}{
		{SchemaCheckpointSubmission, reflect.TypeOf(CheckpointSubmission{})},
		{SchemaGatewayRegistration, reflect.TypeOf(GatewayRegistration{})},
	} {
		t.Run(tc.goType.Name(), func(t *testing.T) {
			t.Parallel()

			schema := loadSchema(t, tc.schemaPath)

			schemaProps := map[string]bool{}
			for name := range mapField(t, schema, "properties") {
				schemaProps[name] = true
			}

			goProps := map[string]bool{}
			goRequired := map[string]bool{}
			for i := range tc.goType.NumField() {
				f := tc.goType.Field(i)
				name, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
				if name == "" || name == "-" {
					t.Fatalf("field %s has no json tag; every wire field must name itself explicitly", f.Name)
				}
				goProps[name] = true
				// A field without omitempty is always on the wire, which is
				// exactly what the schema's "required" means.
				if !strings.Contains(opts, "omitempty") {
					goRequired[name] = true
				}
			}

			for name := range goProps {
				if !schemaProps[name] {
					t.Errorf("the Go type declares %q but %s does not; a control plane validating "+
						"against the schema would reject a message the gateway can send",
						name, tc.schemaPath)
				}
			}
			for name := range schemaProps {
				if !goProps[name] {
					t.Errorf("%s declares %q but the Go type does not; a gateway would silently "+
						"drop a field the schema promises", tc.schemaPath, name)
				}
			}

			schemaRequired := map[string]bool{}
			if raw, ok := schema["required"]; ok {
				for _, v := range raw.([]any) {
					schemaRequired[v.(string)] = true
				}
			}
			if !reflect.DeepEqual(goRequired, schemaRequired) {
				t.Errorf("required fields disagree\n  Go (fields without omitempty): %v\n  %s: %v",
					sortedKeys(goRequired), tc.schemaPath, sortedKeys(schemaRequired))
			}

			// additionalProperties:false is what makes the property comparison
			// above meaningful. Without it the schema accepts anything and the
			// two sides can diverge freely.
			if v, ok := schema["additionalProperties"]; !ok || v != false {
				t.Errorf("%s must set additionalProperties to false, otherwise it constrains nothing",
					tc.schemaPath)
			}
		})
	}
}

// TestWireFormIsStable pins the exact bytes of an encoded submission. Field
// names and timestamp spelling are the contract; a change here is a change to
// a released protocol and should require deleting this expectation on purpose.
func TestWireFormIsStable(t *testing.T) {
	t.Parallel()

	encoded, err := json.Marshal(sampleSubmission())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	const want = `{"gateway_id":"9f8d3a1e-4c2b-4f5a-8e7d-1b2c3d4e5f60","checkpoint_id":42,` +
		`"range_start":10001,"range_end":20000,"event_count":10000,` +
		`"merkle_root":"a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3",` +
		`"prev_checkpoint_id":41,` +
		`"prev_checkpoint_hash":"b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7",` +
		`"checkpoint_hash":"c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1",` +
		`"hash_algorithm":"sha-256","hash_schema_version":1,"canonicalization_spec":"rfc8785-v1",` +
		`"sealed_at":"2026-08-21T14:00:00.123456Z","sealer_version":"1.2.3","gateway_version":"1.2.3",` +
		`"covered_from":"2026-08-21T13:00:00.000000Z","covered_to":"2026-08-21T13:59:59.999999Z"}`
	if string(encoded) != want {
		t.Errorf("the wire form changed\n want: %s\n  got: %s", want, encoded)
	}
}

// TestTimestampPrecisionSurvives covers the reason [TimestampFormat] is pinned:
// sealed_at is hashed as microseconds, so a format that dropped or invented
// precision would make the checkpoint hash unrecomputable from the wire value.
func TestTimestampPrecisionSurvives(t *testing.T) {
	t.Parallel()

	// A nanosecond component that microsecond precision cannot represent.
	original := time.Date(2026, 8, 21, 14, 0, 0, 123456789, time.UTC)
	ts := NewTimestamp(original)

	if got, want := ts.UnixMicro(), original.UnixMicro(); got != want {
		t.Fatalf("construction lost precision: %d micros, want %d", got, want)
	}

	encoded, err := json.Marshal(ts)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Timestamp
	if err := json.Unmarshal(encoded, &out); err != nil {
		t.Fatalf("unmarshal %s: %v", encoded, err)
	}
	if got, want := out.UnixMicro(), original.UnixMicro(); got != want {
		t.Errorf("the round trip moved the instant: %d micros, want %d", got, want)
	}
	if !out.Equal(ts.Time) {
		t.Errorf("the round trip did not reproduce the truncated value: %v then %v", ts.Time, out.Time)
	}
}

func TestTimestampRejectsOtherSpellings(t *testing.T) {
	t.Parallel()

	// Each of these names the same instant as the canonical form, and each is
	// rejected: a verifier recomputing a hash needs one spelling, not several.
	for _, s := range []string{
		`"2026-08-21T14:00:00Z"`,
		`"2026-08-21T14:00:00.123Z"`,
		`"2026-08-21T14:00:00.123456789Z"`,
		`"2026-08-21T15:00:00.123456+01:00"`,
		`"2026-08-21 14:00:00.123456Z"`,
	} {
		var ts Timestamp
		if err := json.Unmarshal([]byte(s), &ts); err == nil {
			t.Errorf("%s was accepted; only %s is on the wire", s, TimestampFormat)
		}
	}
}

func TestValidate_RejectsMalformedSubmissions(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		mutate  func(*CheckpointSubmission)
		wantErr string
	}{
		{"count disagreeing with the range", func(s *CheckpointSubmission) {
			s.EventCount = 9999
		}, "does not match the covered range"},
		{"range running backwards", func(s *CheckpointSubmission) {
			s.RangeStart, s.RangeEnd = 20000, 10001
		}, "precedes range_start"},
		{"a Merkle root of the wrong length", func(s *CheckpointSubmission) {
			s.MerkleRoot = "a3a3"
		}, "merkle_root must be 32 bytes"},
		{"an uppercase digest", func(s *CheckpointSubmission) {
			s.MerkleRoot = HashHex(strings.ToUpper(string(s.MerkleRoot)))
		}, "lowercase"},
		{"a genesis constant on a linked checkpoint", func(s *CheckpointSubmission) {
			s.PrevCheckpointHash = GenesisPrevHash
		}, "genesis constant but prev_checkpoint_id is"},
		{"a predecessor that does not precede", func(s *CheckpointSubmission) {
			later := int64(43)
			s.PrevCheckpointID = &later
		}, "does not precede checkpoint_id"},
		{"an unknown hash algorithm", func(s *CheckpointSubmission) {
			s.HashAlgorithm = "sha3-256"
		}, "is not supported"},
		{"a missing covered range", func(s *CheckpointSubmission) {
			s.CoveredFrom = Timestamp{}
		}, "covered_from is required"},
		{"a covered range running backwards", func(s *CheckpointSubmission) {
			s.CoveredFrom, s.CoveredTo = s.CoveredTo, s.CoveredFrom
		}, "precedes covered_from"},
		{"events covered after the seal", func(s *CheckpointSubmission) {
			s.CoveredFrom = NewTimestamp(s.SealedAt.Add(time.Hour))
			s.CoveredTo = NewTimestamp(s.SealedAt.Add(2 * time.Hour))
		}, "written after it was sealed"},
		{"a populated config_hash", func(s *CheckpointSubmission) {
			s.ConfigHash = HashHex(strings.Repeat("d2", 32))
		}, "config_hash is reserved in v1"},
		{"populated policy bundles", func(s *CheckpointSubmission) {
			s.PolicyBundles = []PolicyBundleRef{{
				Name: "default", Digest: HashHex(strings.Repeat("e4", 32)),
				HashAlgorithm: HashAlgorithmSHA256,
			}}
		}, "policy_bundles is reserved in v1"},
		{"a control character in a label", func(s *CheckpointSubmission) {
			s.SealerVersion = "1.2.3\nsealed"
		}, "control character"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := sampleSubmission()
			tc.mutate(&s)
			err := s.Validate()
			if err == nil {
				t.Fatalf("accepted %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error does not name the problem\n want it to contain: %s\n            got: %v",
					tc.wantErr, err)
			}
		})
	}
}

// TestReservedFieldsAreRejected covers the whole reserved set at once.
//
// Declaring a field and forbidding its use is only a boundary if the
// forbidding is enforced. Otherwise the first emitter to populate one decides
// what it means, in a package that is meant to be a contract.
func TestReservedFieldsAreRejected(t *testing.T) {
	t.Parallel()

	s := sampleSubmissionWithReserved()
	err := s.Validate()
	if err == nil {
		t.Fatal("a submission populating the reserved fields was accepted")
	}
	if !strings.Contains(err.Error(), "reserved in v1") {
		t.Errorf("the error does not say the field is reserved: %v", err)
	}

	// Removing them makes the same message valid, so the rejection is about
	// the reserved fields and nothing else.
	s.ConfigHash = ""
	s.PolicyBundles = nil
	if err := s.Validate(); err != nil {
		t.Errorf("clearing the reserved fields left the message invalid: %v", err)
	}
}

func TestGenesisSubmissionMustCarryTheGenesisConstant(t *testing.T) {
	t.Parallel()

	s := sampleSubmission()
	s.PrevCheckpointID = nil
	err := s.Validate()
	if err == nil || !strings.Contains(err.Error(), "genesis constant") {
		t.Fatalf("a genesis checkpoint with a non-genesis predecessor hash was accepted: %v", err)
	}
}

// assertSchemaShape checks an encoded instance against the parts of the schema
// this package can enforce without a validator: which properties may appear,
// which must appear, and the patterns declared for the scalar types the
// protocol relies on. It is deliberately not a JSON Schema implementation.
func assertSchemaShape(t *testing.T, schemaPath string, encoded []byte) {
	t.Helper()

	schema := loadSchema(t, schemaPath)
	props := mapField(t, schema, "properties")

	var instance map[string]any
	if err := json.Unmarshal(encoded, &instance); err != nil {
		t.Fatalf("the encoded message is not an object: %v", err)
	}

	for name := range instance {
		if _, ok := props[name]; !ok {
			t.Errorf("%s has no property %q, and it sets additionalProperties to false",
				schemaPath, name)
		}
	}
	if raw, ok := schema["required"]; ok {
		for _, v := range raw.([]any) {
			if _, ok := instance[v.(string)]; !ok {
				t.Errorf("%s requires %q, which the encoded message omits", schemaPath, v)
			}
		}
	}

	// Spot-check the two formats the hash chain depends on. A digest that is
	// not 64 lowercase hex characters or a timestamp without exactly six
	// fractional digits breaks recomputation, so they are worth asserting
	// against the schema's own patterns rather than against a copy of them.
	// $defs is absent from schemas that declare no reusable types, which is
	// fine: the loop below only consults it for fields that are present.
	defs, _ := schema["$defs"].(map[string]any)
	for name, want := range map[string]string{
		"merkle_root":          "sha256Hex",
		"prev_checkpoint_hash": "sha256Hex",
		"checkpoint_hash":      "sha256Hex",
		"config_hash":          "sha256Hex",
		"sealed_at":            "microsecondTimestamp",
		"covered_from":         "microsecondTimestamp",
		"covered_to":           "microsecondTimestamp",
	} {
		value, present := instance[name]
		if !present {
			continue
		}
		def, ok := defs[want].(map[string]any)
		if !ok {
			t.Fatalf("%s has no $defs/%s", schemaPath, want)
		}
		pattern, _ := def["pattern"].(string)
		if pattern == "" {
			t.Fatalf("$defs/%s in %s declares no pattern", want, schemaPath)
		}
		if err := matchJSONSchemaPattern(pattern, fmt.Sprint(value)); err != nil {
			t.Errorf("%s value %q does not satisfy $defs/%s: %v", name, value, want, err)
		}
	}
}

func loadSchema(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := Schemas.ReadFile(path)
	if err != nil {
		t.Fatalf("read embedded schema %s: %v", path, err)
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("%s is not valid JSON: %v", path, err)
	}
	return schema
}

func mapField(t *testing.T, obj map[string]any, key string) map[string]any {
	t.Helper()
	v, ok := obj[key].(map[string]any)
	if !ok {
		t.Fatalf("expected object field %q", key)
	}
	return v
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// matchJSONSchemaPattern applies a JSON Schema "pattern" to a value.
//
// JSON Schema patterns are ECMA-262 regexes and Go's regexp is RE2, so the two
// are not interchangeable in general. The patterns this helper is used against
// are plain character classes and quantifiers, which both engines read the
// same way. Anything containing a construct that does not carry over is
// rejected loudly rather than silently reinterpreted.
func matchJSONSchemaPattern(pattern, value string) error {
	if strings.Contains(pattern, `\u`) || strings.Contains(pattern, "(?") {
		return fmt.Errorf("pattern %q uses ECMA-262 syntax this helper does not translate", pattern)
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return fmt.Errorf("pattern %q does not compile: %w", pattern, err)
	}
	if !re.MatchString(value) {
		return fmt.Errorf("%q does not match %s", value, pattern)
	}
	return nil
}
