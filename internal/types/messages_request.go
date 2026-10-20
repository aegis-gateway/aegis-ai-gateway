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
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// MessagesRequest is a decoded POST /v1/messages body: the Anthropic Messages
// API, as Claude Code and the Anthropic SDKs send it.
//
// The gateway does not translate it. It is forwarded to an Anthropic-type
// provider as the client sent it, with only the model rewritten, because the
// constructs Claude Code depends on (thinking blocks whose signature must
// round-trip byte for byte, cache_control markers, effort, context management)
// have no faithful equivalent in the OpenAI-shaped AegisRequest.
//
// What it does instead is govern a projection. Projection builds an
// AegisRequest carrying every client-supplied string that will reach the
// provider, so the same filter chain, validator, router gate, policy input and
// audit record apply. The decoder is an allowlist at every level, in the
// manner of DecodeChatCompletion: a field or block type it does not know is a
// 400 naming it, because a field forwarded unread is an egress path the filters
// do not cover. messages_scan_coverage_test.go holds the projection to that.
type MessagesRequest struct {
	Model         string
	Stream        bool
	MaxTokens     *int
	Temperature   *float64
	TopP          *float64
	StopSequences []string

	System   []MessagesBlock
	Messages []MessagesTurn
	Tools    []MessagesTool

	ToolChoice MessagesToolChoice

	// MetadataUserID is metadata.user_id. Claude Code puts a JSON string with
	// a device id and session id there. It is client text that reaches the
	// provider, so it is scanned like any other.
	MetadataUserID string

	// ConfigStrings are the string leaves of thinking, context_management and
	// output_config, in document order. Those objects are configuration, but
	// they can name tools (context_management exclude lists) and carry a JSON
	// schema (output_config.format), so their text is scanned rather than
	// assumed harmless.
	ConfigStrings []string

	// Raw is the body as received. It is what is forwarded.
	Raw []byte
}

// MessagesTurn is one entry of messages[].
type MessagesTurn struct {
	Role   string
	Blocks []MessagesBlock
	// StringContent records that content arrived as a plain string, carried
	// in Blocks[0] as a text block.
	StringContent bool
}

// MessagesBlock is one content block. Which fields are set depends on Type.
type MessagesBlock struct {
	Type string

	// text, and the text of a thinking block
	Text string

	// image (base64 source only)
	Image *ImageURL

	// tool_use
	ID    string
	Name  string
	Input json.RawMessage

	// tool_result
	ToolUseID string
	Content   []MessagesBlock // text and image blocks only
	IsError   bool

	// thinking: Signature, redacted_thinking: Data. Opaque values the
	// provider issued and verifies. Scanned anyway; see Projection.
	Signature string
	Data      string
}

// MessagesTool is one client tool definition.
type MessagesTool struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

// MessagesToolChoice is tool_choice. Type is one of auto, any, tool, none.
type MessagesToolChoice struct {
	Type string
	Name string
}

// Block types accepted in a message.
const (
	BlockText             = "text"
	BlockImage            = "image"
	BlockToolUse          = "tool_use"
	BlockToolResult       = "tool_result"
	BlockThinking         = "thinking"
	BlockRedactedThinking = "redacted_thinking"
)

var messagesTopFields = map[string]bool{
	"model": true, "messages": true, "system": true, "max_tokens": true, "stream": true,
	"temperature": true, "top_p": true, "top_k": true, "stop_sequences": true,
	"tools": true, "tool_choice": true, "metadata": true,
	"thinking": true, "context_management": true, "output_config": true,
}

var messagesTopRefusals = map[string]string{
	"mcp_servers":   "the provider would connect to MCP servers the gateway never sees, calling tools outside the audit trail",
	"container":     "AEGIS does not forward provider-side code execution containers",
	"service_tier":  "AEGIS does not select a provider service tier",
	"inference_geo": "AEGIS does not forward a data residency selection it cannot verify was honoured",
}

var blockFields = map[string]map[string]bool{
	BlockText:             {"type": true, "text": true, "cache_control": true, "citations": true},
	BlockImage:            {"type": true, "source": true, "cache_control": true},
	BlockToolUse:          {"type": true, "id": true, "name": true, "input": true, "cache_control": true},
	BlockToolResult:       {"type": true, "tool_use_id": true, "content": true, "is_error": true, "cache_control": true},
	BlockThinking:         {"type": true, "thinking": true, "signature": true},
	BlockRedactedThinking: {"type": true, "data": true},
}

var blockRefusals = map[string]string{
	"document":               "AEGIS cannot scan a document block (PDF or plain-text source) and will not forward one unscanned",
	"search_result":          "AEGIS does not forward search result blocks",
	"server_tool_use":        "AEGIS does not forward provider-side tool calls, which run outside the audit trail",
	"web_search_tool_result": "AEGIS does not forward provider-side tool results",
	"web_fetch_tool_result":  "AEGIS does not forward provider-side tool results",
	"mcp_tool_use":           "AEGIS does not forward provider-side MCP tool calls",
	"mcp_tool_result":        "AEGIS does not forward provider-side MCP tool results",
	"container_upload":       "AEGIS does not forward provider-side code execution files",
}

var toolDefFields = map[string]bool{
	"name": true, "description": true, "input_schema": true, "cache_control": true, "type": true,
}

var toolChoiceFields = map[string]bool{"type": true, "name": true, "disable_parallel_tool_use": true}

var cacheControlFields = map[string]bool{"type": true, "ttl": true}

// checkCacheControl allows cache_control only with its documented values. The
// marker is forwarded, and an unchecked string in it would be client text no
// filter reads.
func checkCacheControl(parent map[string]json.RawMessage, path string) error {
	raw, ok := parent["cache_control"]
	if !ok || isNull(raw) {
		return nil
	}
	if err := checkNested(parent, "cache_control", cacheControlFields, path); err != nil {
		return err
	}
	var v struct {
		Type string `json:"type"`
		TTL  string `json:"ttl"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return fmt.Errorf("%s.cache_control: %w", path, err)
	}
	if v.Type != "ephemeral" || (v.TTL != "" && v.TTL != "5m" && v.TTL != "1h") {
		return &UnsupportedFieldError{Field: "cache_control", Path: path,
			Reason: `cache_control must be {"type":"ephemeral"} with an optional ttl of "5m" or "1h"`}
	}
	return nil
}

var imageSourceFields = map[string]bool{"type": true, "media_type": true, "data": true}

// DecodeMessages parses and checks a /v1/messages body.
func DecodeMessages(data []byte) (*MessagesRequest, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if err := checkFields(top, messagesTopFields, messagesTopRefusals, ""); err != nil {
		return nil, err
	}

	req := &MessagesRequest{Raw: data}

	var scalars struct {
		Model         string   `json:"model"`
		Stream        bool     `json:"stream"`
		MaxTokens     *int     `json:"max_tokens"`
		Temperature   *float64 `json:"temperature"`
		TopP          *float64 `json:"top_p"`
		TopK          *int     `json:"top_k"`
		StopSequences []string `json:"stop_sequences"`
	}
	if err := json.Unmarshal(data, &scalars); err != nil {
		return nil, err
	}
	req.Model, req.Stream, req.MaxTokens = scalars.Model, scalars.Stream, scalars.MaxTokens
	req.Temperature, req.TopP, req.StopSequences = scalars.Temperature, scalars.TopP, scalars.StopSequences

	if raw, ok := top["system"]; ok && !isNull(raw) {
		blocks, err := decodeSystem(raw)
		if err != nil {
			return nil, err
		}
		req.System = blocks
	}

	if raw, ok := top["messages"]; ok {
		var turns []map[string]json.RawMessage
		if err := json.Unmarshal(raw, &turns); err != nil {
			return nil, fmt.Errorf("messages: %w", err)
		}
		for i, t := range turns {
			turn, err := decodeTurn(t, fmt.Sprintf("messages[%d]", i))
			if err != nil {
				return nil, err
			}
			req.Messages = append(req.Messages, turn)
		}
	}

	if raw, ok := top["tools"]; ok && !isNull(raw) {
		var tools []map[string]json.RawMessage
		if err := json.Unmarshal(raw, &tools); err != nil {
			return nil, fmt.Errorf("tools: %w", err)
		}
		for i, t := range tools {
			tool, err := decodeTool(t, fmt.Sprintf("tools[%d]", i))
			if err != nil {
				return nil, err
			}
			req.Tools = append(req.Tools, tool)
		}
	}

	if raw, ok := top["tool_choice"]; ok && !isNull(raw) {
		var tc map[string]json.RawMessage
		if err := json.Unmarshal(raw, &tc); err != nil {
			return nil, fmt.Errorf("tool_choice: %w", err)
		}
		if err := checkFields(tc, toolChoiceFields, nil, "tool_choice"); err != nil {
			return nil, err
		}
		var v struct {
			Type string `json:"type"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, fmt.Errorf("tool_choice: %w", err)
		}
		switch v.Type {
		case "auto", "any", "none":
		case "tool":
			if v.Name == "" {
				return nil, fmt.Errorf("%w: tool_choice of type \"tool\" requires name", ErrInvalidToolChoice)
			}
		default:
			return nil, fmt.Errorf("%w: type %q is not one of auto, any, tool, none", ErrInvalidToolChoice, v.Type)
		}
		req.ToolChoice = MessagesToolChoice{Type: v.Type, Name: v.Name}
	}

	if raw, ok := top["metadata"]; ok && !isNull(raw) {
		var md map[string]json.RawMessage
		if err := json.Unmarshal(raw, &md); err != nil {
			return nil, fmt.Errorf("metadata: %w", err)
		}
		if err := checkFields(md, map[string]bool{"user_id": true}, nil, "metadata"); err != nil {
			return nil, err
		}
		var v struct {
			UserID string `json:"user_id"`
		}
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, fmt.Errorf("metadata: %w", err)
		}
		req.MetadataUserID = v.UserID
	}

	for _, key := range []string{"thinking", "context_management", "output_config"} {
		if raw, ok := top[key]; ok && !isNull(raw) {
			strs, err := stringLeaves(raw)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", key, err)
			}
			req.ConfigStrings = append(req.ConfigStrings, strs...)
		}
	}

	return req, nil
}

func isNull(raw json.RawMessage) bool { return strings.TrimSpace(string(raw)) == "null" }

func decodeSystem(raw json.RawMessage) ([]MessagesBlock, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("system: %w", err)
		}
		return []MessagesBlock{{Type: BlockText, Text: s}}, nil
	}
	var elems []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &elems); err != nil {
		return nil, errors.New("system must be a string or an array of text blocks")
	}
	var out []MessagesBlock
	for i, e := range elems {
		path := fmt.Sprintf("system[%d]", i)
		b, err := decodeBlock(e, path)
		if err != nil {
			return nil, err
		}
		if b.Type != BlockText {
			return nil, &UnsupportedFieldError{Field: "type", Path: path,
				Reason: fmt.Sprintf("system accepts only text blocks, got %q", b.Type)}
		}
		out = append(out, b)
	}
	return out, nil
}

func decodeTurn(t map[string]json.RawMessage, path string) (MessagesTurn, error) {
	if err := checkFields(t, map[string]bool{"role": true, "content": true}, nil, path); err != nil {
		return MessagesTurn{}, err
	}
	var role string
	if err := json.Unmarshal(t["role"], &role); err != nil {
		return MessagesTurn{}, fmt.Errorf("%s.role: %w", path, err)
	}
	if role != RoleUser && role != RoleAssistant {
		return MessagesTurn{}, fmt.Errorf("%s.role: %q is not user or assistant", path, role)
	}
	turn := MessagesTurn{Role: role}

	raw := bytes.TrimSpace(t["content"])
	if len(raw) > 0 && raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return MessagesTurn{}, fmt.Errorf("%s.content: %w", path, err)
		}
		turn.Blocks = []MessagesBlock{{Type: BlockText, Text: s}}
		turn.StringContent = true
		return turn, nil
	}
	var elems []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &elems); err != nil {
		return MessagesTurn{}, fmt.Errorf("%s.content must be a string or an array of content blocks", path)
	}
	for i, e := range elems {
		b, err := decodeBlock(e, fmt.Sprintf("%s.content[%d]", path, i))
		if err != nil {
			return MessagesTurn{}, err
		}
		turn.Blocks = append(turn.Blocks, b)
	}
	return turn, nil
}

func decodeBlock(e map[string]json.RawMessage, path string) (MessagesBlock, error) {
	var typ string
	if raw, ok := e["type"]; ok {
		if err := json.Unmarshal(raw, &typ); err != nil {
			return MessagesBlock{}, fmt.Errorf("%s.type: %w", path, err)
		}
	}
	allowed, ok := blockFields[typ]
	if !ok {
		reason := blockRefusals[typ]
		if reason == "" {
			reason = "AEGIS forwards only text, image, tool_use, tool_result, thinking and redacted_thinking blocks, " +
				"and refuses others rather than passing on content no filter has read"
		}
		return MessagesBlock{}, &UnsupportedFieldError{Field: "type=" + typ, Path: path, Reason: reason}
	}
	if err := checkFields(e, allowed, nil, path); err != nil {
		return MessagesBlock{}, err
	}
	if err := checkCacheControl(e, path); err != nil {
		return MessagesBlock{}, err
	}

	b := MessagesBlock{Type: typ}
	switch typ {
	case BlockText:
		var v struct {
			Text      string          `json:"text"`
			Citations json.RawMessage `json:"citations"`
		}
		if err := json.Unmarshal(mustMarshal(e), &v); err != nil {
			return b, fmt.Errorf("%s: %w", path, err)
		}
		// Citations are provider output describing search or document
		// sources, which this gateway refuses. A client resending them would
		// forward text no filter reads; null and [] carry nothing.
		if c := strings.TrimSpace(string(v.Citations)); c != "" && c != "null" && c != "[]" {
			return b, &UnsupportedFieldError{Field: "citations", Path: path,
				Reason: "citations refer to document or search sources AEGIS does not forward"}
		}
		b.Text = v.Text

	case BlockImage:
		img, err := decodeImageSource(e, path)
		if err != nil {
			return b, err
		}
		b.Image = img

	case BlockToolUse:
		var v struct {
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}
		if err := json.Unmarshal(mustMarshal(e), &v); err != nil {
			return b, fmt.Errorf("%s: %w", path, err)
		}
		b.ID, b.Name, b.Input = v.ID, v.Name, v.Input

	case BlockToolResult:
		var v struct {
			ToolUseID string          `json:"tool_use_id"`
			Content   json.RawMessage `json:"content"`
			IsError   bool            `json:"is_error"`
		}
		if err := json.Unmarshal(mustMarshal(e), &v); err != nil {
			return b, fmt.Errorf("%s: %w", path, err)
		}
		b.ToolUseID, b.IsError = v.ToolUseID, v.IsError
		content := bytes.TrimSpace(v.Content)
		switch {
		case len(content) == 0 || string(content) == "null":
		case content[0] == '"':
			var s string
			if err := json.Unmarshal(content, &s); err != nil {
				return b, fmt.Errorf("%s.content: %w", path, err)
			}
			b.Content = []MessagesBlock{{Type: BlockText, Text: s}}
		default:
			var elems []map[string]json.RawMessage
			if err := json.Unmarshal(content, &elems); err != nil {
				return b, fmt.Errorf("%s.content must be a string or an array of blocks", path)
			}
			for i, ce := range elems {
				cpath := fmt.Sprintf("%s.content[%d]", path, i)
				cb, err := decodeBlock(ce, cpath)
				if err != nil {
					return b, err
				}
				if cb.Type != BlockText && cb.Type != BlockImage {
					return b, &UnsupportedFieldError{Field: "type=" + cb.Type, Path: cpath,
						Reason: "a tool result may contain only text and image blocks"}
				}
				b.Content = append(b.Content, cb)
			}
		}

	case BlockThinking:
		var v struct {
			Thinking  string `json:"thinking"`
			Signature string `json:"signature"`
		}
		if err := json.Unmarshal(mustMarshal(e), &v); err != nil {
			return b, fmt.Errorf("%s: %w", path, err)
		}
		b.Text, b.Signature = v.Thinking, v.Signature

	case BlockRedactedThinking:
		var v struct {
			Data string `json:"data"`
		}
		if err := json.Unmarshal(mustMarshal(e), &v); err != nil {
			return b, fmt.Errorf("%s: %w", path, err)
		}
		b.Data = v.Data
	}
	return b, nil
}

// decodeImageSource admits only a base64 source and validates it exactly as an
// OpenAI image_url data URL is validated, so both ingresses gate one thing.
func decodeImageSource(e map[string]json.RawMessage, path string) (*ImageURL, error) {
	// The source type is read first, so a url or file source is refused for
	// what it is rather than for the url key it carries.
	var probe struct {
		Source *struct {
			Type string `json:"type"`
		} `json:"source"`
	}
	if err := json.Unmarshal(mustMarshal(e), &probe); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if probe.Source != nil && probe.Source.Type != "base64" {
		return nil, &MessagesImageError{Path: path, Reason: "only a base64 image source is accepted. A url or file " +
			"source is refused because the provider would fetch content the gateway never sees"}
	}
	if err := checkNested(e, "source", imageSourceFields, path); err != nil {
		return nil, err
	}
	var v struct {
		Source *struct {
			Type      string `json:"type"`
			MediaType string `json:"media_type"`
			Data      string `json:"data"`
		} `json:"source"`
	}
	if err := json.Unmarshal(mustMarshal(e), &v); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if v.Source == nil {
		return nil, &MessagesImageError{Path: path, Reason: "missing source"}
	}
	if v.Source.Type != "base64" {
		return nil, &MessagesImageError{Path: path, Reason: "only a base64 image source is accepted. A url or file " +
			"source is refused because the provider would fetch content the gateway never sees"}
	}
	url := "data:" + v.Source.MediaType + ";base64," + v.Source.Data
	mediaType, n, reason := parseImageDataURL(url)
	if reason != "" {
		return nil, &MessagesImageError{Path: path, Reason: reason}
	}
	return &ImageURL{URL: url, mediaType: mediaType, decodedBytes: n}, nil
}

// MessagesImageError reports an image block AEGIS will not carry. Like
// ImagePartError it never quotes the image or its source.
type MessagesImageError struct {
	Path   string
	Reason string
}

func (e *MessagesImageError) Error() string {
	return fmt.Sprintf("%s is an image block AEGIS will not carry: %s", e.Path, e.Reason)
}

func (e *MessagesImageError) Unwrap() error { return ErrInvalidImagePart }

func decodeTool(t map[string]json.RawMessage, path string) (MessagesTool, error) {
	if raw, ok := t["type"]; ok && !isNull(raw) {
		var typ string
		if err := json.Unmarshal(raw, &typ); err != nil {
			return MessagesTool{}, fmt.Errorf("%s.type: %w", path, err)
		}
		if typ != "custom" {
			return MessagesTool{}, &UnsupportedFieldError{Field: "type=" + typ, Path: path,
				Reason: "only client tools are forwarded. A provider-side tool runs outside the gateway, " +
					"so what it fetches or executes would never reach a filter or the audit trail"}
		}
	}
	if err := checkFields(t, toolDefFields, nil, path); err != nil {
		return MessagesTool{}, err
	}
	if err := checkCacheControl(t, path); err != nil {
		return MessagesTool{}, err
	}
	var v struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"input_schema"`
	}
	if err := json.Unmarshal(mustMarshal(t), &v); err != nil {
		return MessagesTool{}, fmt.Errorf("%s: %w", path, err)
	}
	return MessagesTool{Name: v.Name, Description: v.Description, InputSchema: v.InputSchema}, nil
}

// stringLeaves returns every string value in a JSON document, and every object
// key, in a stable order. Keys are included because a schema's property names
// are as much client text as its values.
func stringLeaves(raw json.RawMessage) ([]string, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	var out []string
	var walk func(any)
	walk = func(x any) {
		switch t := x.(type) {
		case string:
			out = append(out, t)
		case []any:
			for _, e := range t {
				walk(e)
			}
		case map[string]any:
			keys := make([]string, 0, len(t))
			for k := range t {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				out = append(out, k)
				walk(t[k])
			}
		}
	}
	walk(v)
	return out, nil
}

func mustMarshal(m map[string]json.RawMessage) []byte {
	b, _ := json.Marshal(m)
	return b
}

// Projection builds the AegisRequest the governance pipeline reads: filters,
// validator, router gate, policy and audit. It is never forwarded.
//
// The mapping keeps each string in the kind of slot whose scan treatment fits
// it: a tool result becomes a tool-role message, so the injection filter sees
// it as untrusted; a tool_use becomes an assistant tool call; an image becomes
// an image part on a user message, so the unscanned-part gate and its audit
// record see it, including an image returned inside a tool result. Thinking
// text is assistant content. The system blocks become one system message.
// Request-level strings with no AegisRequest home go to IngressMetadata.
func (m *MessagesRequest) Projection() *AegisRequest {
	req := &AegisRequest{
		Model:       m.Model,
		Stream:      m.Stream,
		MaxTokens:   m.MaxTokens,
		Temperature: m.Temperature,
		TopP:        m.TopP,
		Stop:        m.StopSequences,
	}

	if len(m.System) > 0 {
		var texts []string
		for _, b := range m.System {
			texts = append(texts, b.Text)
		}
		req.Messages = append(req.Messages, Message{Role: RoleSystem, Content: PartsContent(texts...)})
	}

	for _, turn := range m.Messages {
		var parts []ContentPart
		var calls []ToolCall
		var results []Message
		var resultImages []ContentPart

		for _, b := range turn.Blocks {
			switch b.Type {
			case BlockText, BlockThinking:
				parts = append(parts, ContentPart{Type: PartText, Text: b.Text})
			case BlockImage:
				parts = append(parts, ContentPart{Type: PartImageURL, ImageURL: b.Image})
			case BlockToolUse:
				calls = append(calls, ToolCall{ID: b.ID, Type: ToolTypeFunction,
					Function: FunctionCallSpec{Name: b.Name, Arguments: string(b.Input)}})
			case BlockToolResult:
				var texts []string
				for _, cb := range b.Content {
					switch cb.Type {
					case BlockText:
						texts = append(texts, cb.Text)
					case BlockImage:
						resultImages = append(resultImages, ContentPart{Type: PartImageURL, ImageURL: cb.Image})
					}
				}
				results = append(results, Message{Role: RoleTool, ToolCallID: b.ToolUseID, Content: PartsContent(texts...)})
			}
		}

		req.Messages = append(req.Messages, results...)
		if len(resultImages) > 0 {
			req.Messages = append(req.Messages, Message{Role: RoleUser,
				Content: Content{Kind: ContentParts, Parts: resultImages}})
		}
		if len(parts) > 0 || len(calls) > 0 {
			req.Messages = append(req.Messages, Message{Role: turn.Role,
				Content: Content{Kind: ContentParts, Parts: parts}, ToolCalls: calls})
		}
	}

	for _, t := range m.Tools {
		req.Tools = append(req.Tools, Tool{Type: ToolTypeFunction, Function: FunctionDef{
			Name: t.Name, Description: t.Description, Parameters: t.InputSchema}})
	}

	switch m.ToolChoice.Type {
	case "auto":
		req.ToolChoice = ToolChoice{Mode: ToolChoiceAuto}
	case "any":
		req.ToolChoice = ToolChoice{Mode: ToolChoiceRequired}
	case "none":
		req.ToolChoice = ToolChoice{Mode: ToolChoiceNone}
	case "tool":
		req.ToolChoice = ToolChoice{Function: m.ToolChoice.Name}
	}

	if m.MetadataUserID != "" {
		req.IngressMetadata = append(req.IngressMetadata, m.MetadataUserID)
	}
	// Thinking signatures and redacted thinking are opaque values the provider
	// issued and verifies, but the client is free to send anything in them,
	// and it leaves the gateway before the provider rejects it. They are
	// scanned for that reason, not because they are expected to hold text.
	for _, turn := range m.Messages {
		for _, b := range turn.Blocks {
			if b.Signature != "" {
				req.IngressMetadata = append(req.IngressMetadata, b.Signature)
			}
			if b.Data != "" {
				req.IngressMetadata = append(req.IngressMetadata, b.Data)
			}
		}
	}
	req.IngressMetadata = append(req.IngressMetadata, m.ConfigStrings...)
	return req
}

// ForwardBody returns the body to send to the provider: the client's body with
// model replaced by the routed provider model. Every other value is carried as
// the client's own bytes, so a thinking signature verifies at the provider.
func (m *MessagesRequest) ForwardBody(providerModel string) ([]byte, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(m.Raw, &top); err != nil {
		return nil, err
	}
	model, err := json.Marshal(providerModel)
	if err != nil {
		return nil, err
	}
	top["model"] = model
	// SetEscapeHTML(false): the default encoder rewrites <, > and & inside the
	// client's strings as \u escapes. The JSON is equivalent, but the point of
	// forwarding natively is that the provider receives what the client sent.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(top); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}
