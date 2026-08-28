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

package types

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// This file makes the build enforce what three rounds of review had to find by
// hand.
//
// The tool-calling work widened the request shape, and each round of review
// found another text channel the previous enumeration had missed: first the
// tool fields themselves, then tool definitions, then tool names, tool_choice's
// function name, and the participant name on a message. Two independent
// reviewers each caught something the author had not. Hand enumeration was not
// converging, and every future widening of the request type is the same
// exercise again.
//
// The move is the one TestNoPayload_SchemaIntrospection already makes for
// payload columns: stop asking a reviewer to remember the rule and make the
// build apply it. Two tests do that here.
//
//   - TestScanSurface_EveryStringFieldIsClassified walks the request type and
//     fails on any string-bearing field that is not explicitly classified as
//     scanned or excluded. A new field cannot be added silently.
//   - TestScanSurface_ScannedFieldsReachTextSegments populates every field
//     classified as scanned with a unique sentinel, decodes through the real
//     wire boundary, and fails if a sentinel does not come back out of
//     TextSegments. Classifying a field as scanned does not make it so.
//
// The pair is what matters. The first alone would let someone add a field and
// classify it as scanned without wiring it up. The second alone would never
// see a field nobody thought to add to the fixture.

// scanDisposition is how a string-bearing field is accounted for.
type scanDisposition int

const (
	// scanned means the field's value must appear in TextSegments, and the
	// second test proves it does.
	scanned scanDisposition = iota
	// excluded means the field is not client-supplied text that reaches a
	// provider. Every exclusion carries a reason, and the reasons are the
	// reviewable part of this file.
	excluded
	// excludedRefusedByDecoder is an exclusion with a machine-checkable
	// justification: the wire allowlist refuses the field, so no client can
	// set it. checkDecoderRefusals below verifies that claim against the
	// decoder rather than trusting the comment.
	excludedRefusedByDecoder
)

type fieldRule struct {
	disposition scanDisposition
	// reason is required for every exclusion. "Not text" is not a reason;
	// say why the value cannot reach a provider as client-controlled text.
	reason string
	// wireName is the top-level JSON key an excludedRefusedByDecoder field
	// would occupy, checked against the decoder's allowlist.
	wireName string
}

// scanSurface classifies every string-bearing field reachable from
// AegisRequest. The walk below discovers the fields; this map says what each
// one is. A field the walk finds and this map does not name fails the build.
//
// Paths are Go field paths from AegisRequest, with [] marking a slice element,
// because Go names are unambiguous where JSON tags are not: several of these
// types carry custom marshallers and their fields have no tags at all.
var scanSurface = map[string]fieldRule{
	// Identity. A client cannot set any of these: the decoder refuses them by
	// name and the handler overwrites them from the authenticated key. They
	// were part of the public request namespace once, which is exactly why
	// they are checked here rather than assumed.
	"AegisRequest.RequestID":      {excludedRefusedByDecoder, "set by the gateway from X-Request-ID", "request_id"},
	"AegisRequest.OrganizationID": {excludedRefusedByDecoder, "set from the authenticated API key", "organization_id"},
	"AegisRequest.TeamID":         {excludedRefusedByDecoder, "set from the authenticated API key", "team_id"},
	"AegisRequest.UserID":         {excludedRefusedByDecoder, "set from the authenticated API key", "user_id"},
	"AegisRequest.APIKeyID":       {excludedRefusedByDecoder, "set from the authenticated API key", "api_key_id"},
	"AegisRequest.Classification": {excludedRefusedByDecoder, "set from the authenticated API key; a body-supplied clearance would be one the caller granted itself", "classification"},
	"AegisRequest.Project":        {excludedRefusedByDecoder, "set from the X-Aegis-Project header", "project"},
	"AegisRequest.TraceContext":   {excludedRefusedByDecoder, "set from the X-Aegis-Trace-Context header", "trace_context"},

	// Routing and control values. These are client-supplied but they are not
	// free text: each is constrained to a vocabulary the gateway itself
	// defines, validates, and matches against, so a secret cannot be smuggled
	// in one and still route.
	"AegisRequest.Model":                           {excluded, "an alias validated against configs/models.yaml; an unmatched value fails routing rather than reaching a provider", ""},
	"AegisRequest.ProviderType":                    {excluded, "set from adapter.Name() after routing, never from the request body", ""},
	"AegisRequest.ToolChoice.Mode":                 {excluded, `one of the literals "none", "auto" or "required"; the decoder refuses any other value`, ""},
	"AegisRequest.Messages[].Role":                 {excluded, "one of a fixed set the validator enforces; an unknown role is a 400", ""},
	"AegisRequest.Messages[].ToolCalls[].Type":     {excluded, `must be the literal "function"; the validator refuses any other value`, ""},
	"AegisRequest.Tools[].Type":                    {excluded, `must be the literal "function"; the validator refuses any other value`, ""},
	"AegisRequest.Messages[].Content.Parts[].Type": {excluded, `must be the literal "text"; a part of any other type is refused at decode`, ""},

	// Everything a client can put words into. Each must come back out of
	// TextSegments, and the second test proves each does.
	"AegisRequest.Stop":                                      {scanned, "", ""},
	"AegisRequest.Messages[].ToolCallID":                     {scanned, "", ""},
	"AegisRequest.Messages[].ToolCalls[].ID":                 {scanned, "", ""},
	"AegisRequest.Messages[].Content.Str":                    {scanned, "", ""},
	"AegisRequest.Messages[].Content.Parts[].Text":           {scanned, "", ""},
	"AegisRequest.Messages[].Name":                           {scanned, "", ""},
	"AegisRequest.Messages[].ToolCalls[].Function.Name":      {scanned, "", ""},
	"AegisRequest.Messages[].ToolCalls[].Function.Arguments": {scanned, "", ""},
	"AegisRequest.Tools[].Function.Name":                     {scanned, "", ""},
	"AegisRequest.Tools[].Function.Description":              {scanned, "", ""},
	"AegisRequest.Tools[].Function.Parameters":               {scanned, "", ""},
	"AegisRequest.ToolChoice.Function":                       {scanned, "", ""},
}

// TestScanSurface_EveryStringFieldIsClassified walks AegisRequest and fails on
// any string-bearing field the map above does not name.
//
// This is the half that cannot be satisfied by remembering. Add a string field
// anywhere in the request type graph and this test names it and stops the
// build until someone decides, in writing, whether it is scanned or why it
// need not be.
func TestScanSurface_EveryStringFieldIsClassified(t *testing.T) {
	t.Parallel()

	found := walkStringFields(reflect.TypeOf(AegisRequest{}), "AegisRequest", map[reflect.Type]bool{})

	for _, path := range found {
		if _, ok := scanSurface[path]; !ok {
			t.Errorf("%s is a string-bearing field reachable from AegisRequest and is not classified.\n"+
				"Add it to scanSurface in this file as either:\n"+
				"  scanned  - client-supplied text that reaches a provider, and therefore must appear in TextSegments\n"+
				"  excluded - with a reason saying why it cannot carry client text to a provider\n"+
				"This test exists because three rounds of review each found a text channel the previous "+
				"enumeration missed. Do not classify a field as excluded to make the build pass.", path)
		}
	}

	// The reverse direction matters too: a stale entry for a field that no
	// longer exists makes the map look more complete than it is.
	inFound := make(map[string]bool, len(found))
	for _, p := range found {
		inFound[p] = true
	}
	for path := range scanSurface {
		if !inFound[path] {
			t.Errorf("scanSurface names %q, which no longer exists on the request type. Remove the entry; "+
				"a stale classification makes the surface look accounted for when it is not", path)
		}
	}

	t.Logf("classified %d string-bearing field(s) reachable from AegisRequest", len(found))
}

// TestScanSurface_ExclusionsAreJustified checks the exclusions rather than
// trusting them.
//
// Every exclusion must carry a reason. An exclusion claiming the decoder
// refuses the field is checked against the decoder's own allowlist, so the
// claim cannot rot when the allowlist changes.
func TestScanSurface_ExclusionsAreJustified(t *testing.T) {
	t.Parallel()

	for path, rule := range scanSurface {
		switch rule.disposition {
		case scanned:
			if rule.reason != "" {
				t.Errorf("%s is classified scanned but carries an exclusion reason; scanned fields need no reason", path)
			}
		case excluded, excludedRefusedByDecoder:
			if strings.TrimSpace(rule.reason) == "" {
				t.Errorf("%s is excluded with no reason. The exclusion list is the reviewable part of this "+
					"file, and an entry without a reason cannot be reviewed", path)
			}
		}

		if rule.disposition != excludedRefusedByDecoder {
			continue
		}
		if rule.wireName == "" {
			t.Errorf("%s claims the decoder refuses it but names no wire field to check", path)
			continue
		}
		// The load-bearing assertion: prove the decoder actually refuses it.
		body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"` +
			rule.wireName + `":"attacker-supplied"}`)
		if _, err := DecodeChatCompletion(body); err == nil {
			t.Errorf("%s is excluded on the grounds that the decoder refuses %q, but a request setting "+
				"that field decoded successfully. Either the allowlist changed or the exclusion was wrong; "+
				"a field a client can set and no filter scans is an unscanned egress path",
				path, rule.wireName)
		}
	}
}

// TestScanSurface_ScannedFieldsReachTextSegments is the behavioural half.
//
// It plants a unique sentinel in every field classified as scanned, decodes the
// result through DecodeChatCompletion so the wire boundary is the real one, and
// asserts each sentinel comes back out of TextSegments. Classifying a field as
// scanned is a claim; this is the check.
func TestScanSurface_ScannedFieldsReachTextSegments(t *testing.T) {
	t.Parallel()

	// One sentinel per scanned field, so a miss names the field that leaked
	// rather than merely reporting that something did.
	body := `{
	  "model": "SENTINEL-model-excluded",
	  "stop": ["SENTINEL_stop_sequence"],
	  "tools": [{
	    "type": "function",
	    "function": {
	      "name": "SENTINEL_tools_function_name",
	      "description": "SENTINEL_tools_function_description",
	      "parameters": {"schema": "SENTINEL_tools_function_parameters"}
	    }
	  }],
	  "tool_choice": {"type": "function", "function": {"name": "SENTINEL_toolchoice_function"}},
	  "messages": [
	    {"role": "user", "name": "SENTINEL_message_name", "content": "SENTINEL_content_str"},
	    {"role": "user", "content": [{"type": "text", "text": "SENTINEL_content_part_text"}]},
	    {"role": "assistant", "content": null, "tool_calls": [{
	      "id": "SENTINEL_toolcall_id", "type": "function",
	      "function": {
	        "name": "SENTINEL_toolcall_function_name",
	        "arguments": "{\"k\":\"SENTINEL_toolcall_function_arguments\"}"
	      }
	    }]},
	    {"role": "tool", "tool_call_id": "SENTINEL_toolcallid", "content": "SENTINEL_tool_result_content"}
	  ]
	}`

	req, err := DecodeChatCompletion([]byte(body))
	if err != nil {
		t.Fatalf("the coverage fixture no longer decodes: %v\n"+
			"If a field was renamed or the allowlist tightened, update the fixture. Do not delete the case.", err)
	}

	// Map each scanned field path to the sentinel that field carries in the
	// fixture. A scanned field with no sentinel here is a hole in the test
	// rather than in the code, and is reported as such.
	sentinels := map[string]string{
		"AegisRequest.Stop":                                      "SENTINEL_stop_sequence",
		"AegisRequest.Messages[].ToolCallID":                     "SENTINEL_toolcallid",
		"AegisRequest.Messages[].ToolCalls[].ID":                 "SENTINEL_toolcall_id",
		"AegisRequest.Messages[].Content.Str":                    "SENTINEL_content_str",
		"AegisRequest.Messages[].Content.Parts[].Text":           "SENTINEL_content_part_text",
		"AegisRequest.Messages[].Name":                           "SENTINEL_message_name",
		"AegisRequest.Messages[].ToolCalls[].Function.Name":      "SENTINEL_toolcall_function_name",
		"AegisRequest.Messages[].ToolCalls[].Function.Arguments": "SENTINEL_toolcall_function_arguments",
		"AegisRequest.Tools[].Function.Name":                     "SENTINEL_tools_function_name",
		"AegisRequest.Tools[].Function.Description":              "SENTINEL_tools_function_description",
		"AegisRequest.Tools[].Function.Parameters":               "SENTINEL_tools_function_parameters",
		"AegisRequest.ToolChoice.Function":                       "SENTINEL_toolchoice_function",
	}

	// Excluded fields that can nonetheless hold arbitrary text get a sentinel
	// too, and are asserted absent. This is the direction the test was missing.
	//
	// It was missing at the exact moment it mattered: a change moved Stop and
	// both tool call correlators into TextSegments and added their sentinels,
	// but left all three classified excluded, with the reasons that change had
	// just rejected. Every test stayed green, because nothing compared an
	// exclusion against what TextSegments actually emits. The map is the
	// reviewable artifact of this file, and it documented the opposite of the
	// behaviour for two commits.
	excludedSentinels := map[string]string{
		"AegisRequest.Model": "SENTINEL-model-excluded",
	}

	for path, rule := range scanSurface {
		switch rule.disposition {
		case scanned:
			if _, ok := sentinels[path]; !ok {
				t.Errorf("%s is classified scanned but the fixture plants no sentinel for it, so nothing "+
					"here proves it is scanned. Add a sentinel to the fixture above", path)
			}
			if _, ok := excludedSentinels[path]; ok {
				t.Errorf("%s is classified scanned but appears in excludedSentinels", path)
			}
		case excluded, excludedRefusedByDecoder:
			if _, ok := sentinels[path]; ok {
				t.Errorf("%s is classified excluded, but the fixture plants a scanned-sentinel for it. "+
					"The classification and the behaviour disagree, and the classification is what a "+
					"reader reviews. Decide which is right and make both say it", path)
			}
		}
	}

	// The tool result content deliberately has no scanSurface entry of its own:
	// it is the same Go field as Messages[].Content.Str, reached with role
	// "tool". It is asserted anyway, because it is the highest-risk surface in
	// the whole request and the one an indirect prompt injection arrives on.
	sentinels["(tool result, via Messages[].Content.Str with role=tool)"] = "SENTINEL_tool_result_content"

	got := req.TextSegments()
	var seen []string
	for _, s := range got {
		seen = append(seen, s.Text)
	}
	haystack := strings.Join(seen, "\x00")

	var missing []string
	for path, sentinel := range sentinels {
		if !strings.Contains(haystack, sentinel) {
			missing = append(missing, fmt.Sprintf("  %s (sentinel %s)", path, sentinel))
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("these fields are classified scanned but their values never reach TextSegments, so no "+
			"filter reads them and their contents egress to the provider unscanned:\n%s\n\n"+
			"TextSegments returned %d segment(s): %v", strings.Join(missing, "\n"), len(got), seen)
	}

	// The inverse. An excluded field whose value turns up in TextSegments means
	// the map and the code disagree, and the map is what a reviewer reads.
	for path, sentinel := range excludedSentinels {
		if strings.Contains(haystack, sentinel) {
			t.Errorf("%s is classified excluded but its value reached TextSegments. Either the field is "+
				"client text and the classification is wrong, or the classification is right and "+
				"something is scanning a field it need not. Both readings need a change, not a "+
				"reclassification to whichever makes this pass", path)
		}
	}

	t.Logf("all %d scanned field(s) reached TextSegments across %d segment(s); %d excluded field(s) stayed out",
		len(sentinels), len(got), len(excludedSentinels))
}

// walkStringFields returns the Go field paths of every string-bearing field
// reachable from typ.
//
// String-bearing means: a string, a named string type, a slice of strings, or a
// []byte such as json.RawMessage, which is how a JSON Schema document rides
// along on a tool definition. Numbers, booleans and times cannot carry a
// credential and are not walked into.
func walkStringFields(typ reflect.Type, path string, visiting map[reflect.Type]bool) []string {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct || visiting[typ] {
		return nil
	}
	// Guard against a recursive type graph rather than assuming there is none.
	visiting[typ] = true
	defer delete(visiting, typ)

	var out []string
	for i := range typ.NumField() {
		f := typ.Field(i)
		if !f.IsExported() {
			continue
		}
		out = append(out, walkFieldType(f.Type, path+"."+f.Name, visiting)...)
	}
	sort.Strings(out)
	return out
}

func walkFieldType(ft reflect.Type, path string, visiting map[reflect.Type]bool) []string {
	for ft.Kind() == reflect.Pointer {
		ft = ft.Elem()
	}

	switch ft.Kind() {
	case reflect.String:
		return []string{path}

	case reflect.Slice, reflect.Array:
		elem := ft.Elem()
		// []byte and json.RawMessage are one value, not a list of them.
		if elem.Kind() == reflect.Uint8 {
			return []string{path}
		}
		if elem.Kind() == reflect.String {
			return []string{path}
		}
		return walkFieldType(elem, path+"[]", visiting)

	case reflect.Struct:
		// time.Time and friends have no client-supplied text in them, and
		// walking in produces noise rather than coverage.
		if ft.PkgPath() != "" && !strings.HasSuffix(ft.PkgPath(), "/internal/types") {
			return nil
		}
		return walkStringFields(ft, path, visiting)

	case reflect.Map:
		// No map-typed field exists on the request today. If one is added it is
		// a text channel until proven otherwise, so fail rather than skip.
		return []string{path + "{}"}

	case reflect.Interface:
		// Same reasoning: an interface field can hold anything, including text.
		return []string{path + "(interface)"}

	default:
		return nil
	}
}

// TestScanSurface_RawMessageIsTreatedAsText guards an assumption the walk above depends on: that
// json.RawMessage presents as a []byte and is therefore reported as one
// string-bearing field rather than skipped.
func TestScanSurface_RawMessageIsTreatedAsText(t *testing.T) {
	t.Parallel()

	got := walkFieldType(reflect.TypeOf(json.RawMessage{}), "probe", map[reflect.Type]bool{})
	if len(got) != 1 || got[0] != "probe" {
		t.Fatalf("json.RawMessage walked to %v, want [probe]. Tool parameter schemas ride in a "+
			"json.RawMessage, and a walk that skips it would leave that field unclassified and unnoticed", got)
	}
}
