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
	"errors"
	"strings"
	"testing"
)

func TestDecodeMessages_ClaudeCodeShapeAccepted(t *testing.T) {
	t.Parallel()
	req, err := DecodeMessages([]byte(messagesFixture))
	if err != nil {
		t.Fatalf("refused: %v", err)
	}
	if req.Model != "aegis-claude" || !req.Stream || len(req.System) != 2 || len(req.Tools) != 1 || len(req.Messages) != 3 {
		t.Errorf("decoded %+v", req)
	}
	p := req.Projection()
	if !p.HasTools() || p.ToolChoice.Function != "S_tool_choice_name" {
		t.Errorf("projection lost tools: %+v", p.ToolChoice)
	}
	// The tool result is a tool-role message, so the injection filter treats
	// it as untrusted content.
	var untrusted bool
	for _, s := range p.TextSegments() {
		if s.Text == "S_tool_result_text" && s.IsUntrusted() {
			untrusted = true
		}
	}
	if !untrusted {
		t.Error("tool result text is not marked untrusted in the projection")
	}
}

func TestDecodeMessages_SystemAsString(t *testing.T) {
	t.Parallel()
	req, err := DecodeMessages([]byte(`{"model":"m","max_tokens":10,"system":"be brief","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(req.System) != 1 || req.System[0].Text != "be brief" {
		t.Errorf("system = %+v", req.System)
	}
}

// Every refusal names what it refused, and none is a silent drop.
func TestDecodeMessages_Refusals(t *testing.T) {
	t.Parallel()
	msg := func(content string) string {
		return `{"model":"m","max_tokens":10,"messages":[{"role":"user","content":[` + content + `]}]}`
	}
	cases := map[string]struct{ body, want string }{
		"unknown top-level field": {`{"model":"m","max_tokens":10,"messages":[],"frobnicate":1}`, "frobnicate"},
		"mcp_servers":             {`{"model":"m","max_tokens":10,"messages":[],"mcp_servers":[]}`, "mcp_servers"},
		"document block":          {msg(`{"type":"document","source":{"type":"text","media_type":"text/plain","data":"x"}}`), "document"},
		"unknown block field":     {msg(`{"type":"text","text":"x","extra":1}`), "extra"},
		"server tool":             {`{"model":"m","max_tokens":10,"messages":[],"tools":[{"type":"web_search_20250305","name":"web_search"}]}`, "web_search_20250305"},
		"unknown tool field":      {`{"model":"m","max_tokens":10,"messages":[],"tools":[{"name":"a","input_schema":{},"defer":true}]}`, "defer"},
		"url image":               {msg(`{"type":"image","source":{"type":"url","url":"https://example.invalid/a.png"}}`), "base64"},
		"bad cache ttl":           {msg(`{"type":"text","text":"x","cache_control":{"type":"ephemeral","ttl":"S_free_text"}}`), "cache_control"},
		"citations":               {msg(`{"type":"text","text":"x","citations":[{"type":"char_location"}]}`), "citations"},
		"system role":             {`{"model":"m","max_tokens":10,"messages":[{"role":"system","content":"x"}]}`, "system"},
		"tool_use in tool_result": {msg(`{"type":"tool_result","tool_use_id":"t","content":[{"type":"tool_use","id":"a","name":"b","input":{}}]}`), "tool_use"},
		"image in system":         {`{"model":"m","max_tokens":10,"system":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + messagesFixturePNG + `"}}],"messages":[]}`, "only text"},
		"bad tool_choice":         {`{"model":"m","max_tokens":10,"messages":[],"tool_choice":{"type":"sometimes"}}`, "sometimes"},
	}
	for name, c := range cases {
		_, err := DecodeMessages([]byte(c.body))
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %q does not name %q", name, err, c.want)
		}
		if strings.Contains(err.Error(), "S_free_text") || strings.Contains(err.Error(), "example.invalid") {
			t.Errorf("%s: error echoes client text: %v", name, err)
		}
	}

	_, err := DecodeMessages([]byte(msg(`{"type":"image","source":{"type":"url","url":"https://x"}}`)))
	if !errors.Is(err, ErrInvalidImagePart) {
		t.Errorf("url image: %v, want ErrInvalidImagePart", err)
	}
}

// The forwarded body is the client's body with only model replaced. A thinking
// signature, cache_control and every unmodelled config value survive, and HTML
// characters are not escaped.
func TestDecodeMessages_ForwardBodyChangesOnlyModel(t *testing.T) {
	t.Parallel()
	in := `{"model":"aegis-claude","max_tokens":10,"output_config":{"effort":"high"},"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"a < b && c > d","signature":"EqQBCkYIBxgCKkA="}]},{"role":"user","content":"<tag>"}]}`
	req, err := DecodeMessages([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	out, err := req.ForwardBody("claude-haiku-4-5-20251001")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "\\"+"u003c") || !strings.Contains(string(out), `"a < b && c > d"`) {
		t.Errorf("text was re-escaped: %s", out)
	}
	var a, b map[string]any
	_ = json.Unmarshal([]byte(in), &a)
	_ = json.Unmarshal(out, &b)
	if b["model"] != "claude-haiku-4-5-20251001" {
		t.Errorf("model = %v", b["model"])
	}
	a["model"], b["model"] = nil, nil
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if string(ja) != string(jb) {
		t.Errorf("body changed beyond model:\n in %s\nout %s", ja, jb)
	}
}
