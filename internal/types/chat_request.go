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
	"fmt"
	"sort"
	"strings"
)

// ChatCompletionRequest is the wire shape of POST /v1/chat/completions.
//
// It is an allowlist. A field of the OpenAI chat completions schema that does
// not appear here is refused at decode, by name, with 400. That is a deliberate
// reversal: the gateway used to decode client JSON straight into AegisRequest,
// so every key without a matching field was discarded in silence, and a request
// carrying tools was answered as though it had carried none. For a product
// whose job is to decide whether a call is permitted, accepting input it does
// not understand is the wrong direction to fail in.
//
// See docs/reference/request-field-support.md for the field-by-field decision
// and the reasoning behind each refusal.
type ChatCompletionRequest struct {
	Model    string        `json:"model"`
	Messages []WireMessage `json:"messages"`

	Temperature *float64  `json:"temperature"`
	MaxTokens   *int      `json:"max_tokens"`
	TopP        *float64  `json:"top_p"`
	Stop        StopField `json:"stop"`
	Stream      bool      `json:"stream"`

	Tools             []Tool     `json:"tools"`
	ToolChoice        ToolChoice `json:"tool_choice"`
	ParallelToolCalls *bool      `json:"parallel_tool_calls"`
}

// WireMessage is the wire shape of one element of messages. Same allowlist
// rule: an unknown key inside a message is refused, not ignored.
type WireMessage struct {
	Role       string     `json:"role"`
	Content    Content    `json:"content"`
	Name       string     `json:"name"`
	ToolCalls  []ToolCall `json:"tool_calls"`
	ToolCallID string     `json:"tool_call_id"`
}

// StopField is the stop parameter, which OpenAI defines as either a single
// string or an array of up to four.
type StopField []string

func (s *StopField) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "null" || trimmed == "" {
		*s = nil
		return nil
	}
	if trimmed[0] == '"' {
		var one string
		if err := json.Unmarshal(data, &one); err != nil {
			return err
		}
		*s = StopField{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(data, &many); err != nil {
		return errors.New("stop must be a string or an array of strings")
	}
	*s = many
	return nil
}

// UnsupportedFieldError reports a request field the gateway refuses.
//
// Reason is written for whoever has to act on the 400. "Unsupported" alone
// sends them to the source; naming what would happen if it were accepted tells
// them whether to drop the field or stop using the gateway for that call.
type UnsupportedFieldError struct {
	Field  string
	Path   string // where the field appeared, e.g. "messages[2]"
	Reason string
}

func (e *UnsupportedFieldError) Error() string {
	where := e.Field
	if e.Path != "" {
		where = e.Path + "." + e.Field
	}
	if e.Reason == "" {
		return fmt.Sprintf("unsupported request field %q: AEGIS refuses fields it cannot honour "+
			"rather than discarding them silently", where)
	}
	return fmt.Sprintf("unsupported request field %q: %s", where, e.Reason)
}

// ErrUnsupportedField lets callers test for the class without matching text.
var ErrUnsupportedField = errors.New("unsupported request field")

func (e *UnsupportedFieldError) Unwrap() error { return ErrUnsupportedField }

// topLevelFields is the set of keys DecodeChatCompletion accepts at the top
// level of the request body.
var topLevelFields = map[string]bool{
	"model": true, "messages": true,
	"temperature": true, "max_tokens": true, "top_p": true, "stop": true, "stream": true,
	"tools": true, "tool_choice": true, "parallel_tool_calls": true,
}

// messageFields is the set of keys accepted inside a message object.
var messageFields = map[string]bool{
	"role": true, "content": true, "name": true,
	"tool_calls": true, "tool_call_id": true,
}

// refusalReasons explains, per field, what accepting-and-ignoring would have
// cost the caller. Any field not listed gets the generic message.
//
// The point of writing these out is that a 400 naming only the field leaves the
// caller to guess whether the option is coming later, is unsafe here, or was
// never real. Each line below answers that.
var refusalReasons = map[string]string{
	"n":                     "AEGIS returns exactly one completion. Accepting n and returning one would answer a different question than the one asked",
	"response_format":       "AEGIS does not negotiate structured output with the provider. A request for JSON mode that was accepted and dropped would return prose the caller then fails to parse",
	"seed":                  "AEGIS does not forward the determinism hint. Accepting it would let a caller believe a run is reproducible when it is not",
	"logprobs":              "AEGIS does not return token log probabilities",
	"top_logprobs":          "AEGIS does not return token log probabilities",
	"logit_bias":            "AEGIS does not forward sampling bias",
	"presence_penalty":      "AEGIS does not forward sampling penalties",
	"frequency_penalty":     "AEGIS does not forward sampling penalties",
	"max_completion_tokens": "use max_tokens. Accepting the newer spelling and dropping it would leave the request with no length limit at all",
	"store":                 "this asks the provider to retain the exchange. AEGIS will not pass a retention instruction it cannot audit, and will not drop one either",
	"metadata":              "AEGIS attributes requests from the API key and the X-Aegis-Project header, not from a client-supplied object",
	"user":                  "AEGIS derives the acting identity from the API key. A second, unverified identity in the body would appear in no audit record and gate no policy",
	"safety_identifier":     "AEGIS derives the acting identity from the API key",
	"prompt_cache_key":      "AEGIS does not manage provider-side prompt caching",
	"stream_options":        "AEGIS does not forward streaming options. Usage in the stream is reported from whatever the provider sends unprompted",
	"functions":             "the deprecated functions API is not supported. Use tools",
	"function_call":         "the deprecated function_call API is not supported. Use tool_choice",
	"service_tier":          "AEGIS does not select a provider service tier",
	"modalities":            "AEGIS handles text only",
	"audio":                 "AEGIS handles text only",
	"prediction":            "AEGIS does not forward predicted outputs",
	"reasoning_effort":      "AEGIS does not forward reasoning effort",
	"verbosity":             "AEGIS does not forward verbosity",
	"web_search_options":    "AEGIS does not enable provider-side web search. A tool the gateway cannot see called is a capability outside the audit trail",
	"request_id":            "set by the gateway. Supply X-Request-ID as a header if you need to choose it",
	"organization_id":       "set from the authenticated API key and not accepted from the body",
	"team_id":               "set from the authenticated API key and not accepted from the body",
	"user_id":               "set from the authenticated API key and not accepted from the body",
	"api_key_id":            "set from the authenticated API key and not accepted from the body",
	"classification":        "set from the authenticated API key and not accepted from the body. A body-supplied classification would be a clearance a caller granted itself",
	"project":               "supply the X-Aegis-Project header. A body value was previously parsed and then overwritten by the header",
	"prefer_provider":       "not implemented. The X-Aegis-Prefer-Provider header is read but never consulted by routing",
	"trace_context":         "supply the X-Aegis-Trace-Context header",
	"skip_cache":            "AEGIS does not cache completions, so there is nothing to skip",
	"refusal":               "assistant refusal text is a provider output field and is not accepted on an inbound message",
}

// messageRefusalReasons covers keys refused specifically inside a message.
var messageRefusalReasons = map[string]string{
	"function_call": "the deprecated function_call API is not supported. Use tool_calls",
	"refusal":       "assistant refusal text is a provider output field and is not accepted on an inbound message",
	"audio":         "AEGIS handles text only",
}

// DecodeChatCompletion parses a chat completions request body into an
// AegisRequest, refusing any field outside the allowlist.
//
// Identity, classification and the AEGIS header fields are deliberately not
// populated here: the caller sets them from the auth context afterwards, and
// this function refuses them if they appear in the body.
func DecodeChatCompletion(data []byte) (*AegisRequest, error) {
	// Two passes. The first sees the raw keys, which is the only way to name
	// the offending field; the second binds values. json.Decoder's
	// DisallowUnknownFields would reject the same requests but reports
	// `json: unknown field "seed"` with no path and no reason, and it cannot
	// distinguish a field AEGIS refuses on purpose from a typo.
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}

	if err := checkFields(top, topLevelFields, refusalReasons, ""); err != nil {
		return nil, err
	}

	// messages is checked key by key too: an unknown key inside a message is
	// exactly how tool_calls and tool_call_id were lost.
	if rawMsgs, ok := top["messages"]; ok {
		var elems []map[string]json.RawMessage
		if err := json.Unmarshal(rawMsgs, &elems); err != nil {
			return nil, fmt.Errorf("messages: %w", err)
		}
		for i, m := range elems {
			if err := checkFields(m, messageFields, messageRefusalReasons, fmt.Sprintf("messages[%d]", i)); err != nil {
				return nil, err
			}
		}
	}

	var wire ChatCompletionRequest
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, err
	}

	req := &AegisRequest{
		Model:             wire.Model,
		Temperature:       wire.Temperature,
		MaxTokens:         wire.MaxTokens,
		TopP:              wire.TopP,
		Stop:              []string(wire.Stop),
		Stream:            wire.Stream,
		Tools:             wire.Tools,
		ToolChoice:        wire.ToolChoice,
		ParallelToolCalls: wire.ParallelToolCalls,
	}
	req.Messages = make([]Message, 0, len(wire.Messages))
	for _, m := range wire.Messages {
		req.Messages = append(req.Messages, Message{
			Role:       m.Role,
			Content:    m.Content,
			Name:       m.Name,
			ToolCalls:  m.ToolCalls,
			ToolCallID: m.ToolCallID,
		})
	}

	return req, nil
}

// checkFields refuses the first key outside the allowlist, in sorted order so
// that a request with several bad fields reports the same one every time.
func checkFields(got map[string]json.RawMessage, allowed map[string]bool, reasons map[string]string, path string) error {
	var bad []string
	for k := range got {
		if !allowed[k] {
			bad = append(bad, k)
		}
	}
	if len(bad) == 0 {
		return nil
	}
	sort.Strings(bad)
	reason := reasons[bad[0]]
	if reason == "" && path != "" {
		reason = refusalReasons[bad[0]]
	}
	err := &UnsupportedFieldError{Field: bad[0], Path: path, Reason: reason}
	if len(bad) > 1 {
		return fmt.Errorf("%w (also refused: %s)", err, strings.Join(bad[1:], ", "))
	}
	return err
}

// MetricFieldLabel maps a refused field name onto a bounded label set.
//
// A refused name is client input. Using it directly as a Prometheus label would
// let a caller mint a new time series per request, so anything AEGIS does not
// recognise collapses to "other".
func MetricFieldLabel(field string) string {
	if refusalReasons[field] != "" || topLevelFields[field] || messageFields[field] {
		return field
	}
	if messageRefusalReasons[field] != "" {
		return field
	}
	return "other"
}
