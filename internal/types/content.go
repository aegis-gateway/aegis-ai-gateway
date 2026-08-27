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
	"strings"
)

// ContentKind records which JSON shape a message's content field arrived in.
//
// The distinction is kept rather than normalised away because it has to survive
// back out to the provider: an assistant turn that carried tool calls has
// content JSON null, and rewriting that as "" changes the conversation the
// provider is asked to continue.
type ContentKind uint8

const (
	// ContentAbsent means the content key was missing or JSON null.
	ContentAbsent ContentKind = iota
	// ContentString means content was a JSON string.
	ContentString
	// ContentParts means content was a JSON array of content parts.
	ContentParts
)

// ContentPartType is the discriminator on a structured content part.
type ContentPartType string

const (
	// PartText is the only content part type AEGIS accepts. Every other type
	// names data the filter chain cannot read; see ErrNonTextContentPart.
	PartText ContentPartType = "text"
)

// ContentPart is one element of a structured content array.
//
// Only text parts are representable. A part of any other type is refused at
// decode by UnmarshalJSON rather than carried in a field nothing inspects,
// because a content part that reaches a provider unread is an egress path the
// secrets, PII and injection filters do not cover.
type ContentPart struct {
	Type ContentPartType `json:"type"`
	Text string          `json:"text"`
}

// ErrNonTextContentPart is returned when a content array carries a part whose
// type is not "text". It is a named error so the HTTP layer can turn it into a
// specific 400 rather than a generic JSON parse failure.
var ErrNonTextContentPart = errors.New("non-text content part")

// NonTextPartError reports the rejected part type and its position.
type NonTextPartError struct {
	Index int
	Type  string
}

func (e *NonTextPartError) Error() string {
	t := e.Type
	if t == "" {
		t = "(missing type)"
	}
	return fmt.Sprintf("content[%d] has type %q: AEGIS accepts only text content parts, "+
		"because it cannot scan any other kind for secrets, PII or prompt injection, "+
		"and an unscannable part would leave the gateway for the provider unfiltered", e.Index, t)
}

func (e *NonTextPartError) Unwrap() error { return ErrNonTextContentPart }

// Content is an OpenAI message content field: a string, an array of content
// parts, or null.
//
// Text is never read directly by the filter chain. Filters call TextSegments on
// the message so that every text-bearing element is scanned, not only whichever
// one a given call site happened to reach for.
type Content struct {
	Kind  ContentKind
	Str   string
	Parts []ContentPart
}

// TextContent builds string-shaped content. Used by the response path and by
// tests; the request path builds Content by unmarshalling.
func TextContent(s string) Content {
	return Content{Kind: ContentString, Str: s}
}

// PartsContent builds array-shaped content from text parts.
func PartsContent(texts ...string) Content {
	parts := make([]ContentPart, 0, len(texts))
	for _, t := range texts {
		parts = append(parts, ContentPart{Type: PartText, Text: t})
	}
	return Content{Kind: ContentParts, Parts: parts}
}

// IsEmpty reports whether the content carries no text at all.
func (c Content) IsEmpty() bool {
	switch c.Kind {
	case ContentString:
		return c.Str == ""
	case ContentParts:
		return len(c.Parts) == 0
	default:
		return true
	}
}

// Texts returns every text string the content carries, in order.
//
// A string content yields one element; an array yields one per part. It returns
// a slice rather than a joined string so that a caller which scans per element
// (the PII service, which is called per text) is not forced to concatenate, and
// so a secret split across a part boundary is not manufactured by joining.
func (c Content) Texts() []string {
	switch c.Kind {
	case ContentString:
		if c.Str == "" {
			return nil
		}
		return []string{c.Str}
	case ContentParts:
		out := make([]string, 0, len(c.Parts))
		for _, p := range c.Parts {
			if p.Text != "" {
				out = append(out, p.Text)
			}
		}
		return out
	default:
		return nil
	}
}

// Flatten joins the content's text with newlines.
//
// For the adapters that can only express a single string (the Anthropic system
// prompt) and for length accounting. It is deliberately not what the filters
// use: joining could create a match spanning two parts that exists in neither.
func (c Content) Flatten() string {
	texts := c.Texts()
	switch len(texts) {
	case 0:
		return ""
	case 1:
		return texts[0]
	default:
		return strings.Join(texts, "\n")
	}
}

// UnmarshalJSON accepts a JSON string, a JSON array of content parts, or null.
//
// A non-text part is an error, not a silently retained field. See
// NonTextPartError.
func (c *Content) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "null" || trimmed == "" {
		*c = Content{Kind: ContentAbsent}
		return nil
	}

	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		*c = Content{Kind: ContentString, Str: s}
		return nil
	}

	if trimmed[0] != '[' {
		return errors.New("content must be a string or an array of content parts")
	}

	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	parts := make([]ContentPart, 0, len(raw))
	for i, elem := range raw {
		// Decode the discriminator first so a non-text part is reported by its
		// own type rather than by whichever field failed to bind.
		var probe struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(elem, &probe); err != nil {
			return fmt.Errorf("content[%d]: %w", i, err)
		}
		if ContentPartType(probe.Type) != PartText {
			return &NonTextPartError{Index: i, Type: probe.Type}
		}

		var part ContentPart
		dec := json.NewDecoder(strings.NewReader(string(elem)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&part); err != nil {
			return fmt.Errorf("content[%d]: %w", i, err)
		}
		parts = append(parts, part)
	}

	*c = Content{Kind: ContentParts, Parts: parts}
	return nil
}

// MarshalJSON re-emits the shape the content arrived in, so that what leaves
// for the provider is what the client sent.
func (c Content) MarshalJSON() ([]byte, error) {
	switch c.Kind {
	case ContentString:
		return json.Marshal(c.Str)
	case ContentParts:
		if c.Parts == nil {
			return []byte("[]"), nil
		}
		return json.Marshal(c.Parts)
	default:
		return []byte("null"), nil
	}
}
