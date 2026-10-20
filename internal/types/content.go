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
	"encoding/base64"
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
	// PartText is a text part. Every filter reads it.
	PartText ContentPartType = "text"
	// PartImageURL is an image part. No filter reads it: the gateway has no
	// image scanner, and text extracted from an image would cover only the
	// text in it. It is representable so that it can be routed, and admitted
	// only on a route whose operator has opted that route into carrying
	// unscanned parts. See ContentPart and router.ResolveRouteFor.
	PartImageURL ContentPartType = "image_url"
)

// UnscannedKindImage is the name an operator uses for image parts in a
// route's unscanned_parts list in configs/models.yaml.
const UnscannedKindImage = "image"

// ContentPart is one element of a structured content array.
//
// Text and image parts are representable. Any other type is refused at decode
// by UnmarshalJSON rather than carried in a field nothing inspects, because a
// content part that reaches a provider unread is an egress path the secrets,
// PII and injection filters do not cover.
//
// An image part is exactly such an egress path, and is representable anyway
// because it is gated rather than scanned: the router admits it only on a
// route that lists it in unscanned_parts, and the audit trail records that it
// left (count, decoded bytes, media type; never content or a content hash).
type ContentPart struct {
	Type     ContentPartType
	Text     string
	ImageURL *ImageURL
}

// ImageURL is the image_url object of an image part.
//
// URL is only ever a base64 data URL of an allowlisted image type: UnmarshalJSON
// refuses a remote URL, because the provider would fetch content the gateway
// never sees, and the URL itself is caller text no filter reads.
type ImageURL struct {
	URL    string
	Detail string

	// Set at decode from the validated URL. Unexported so they are not part of
	// the wire shape, and so nothing outside this package can make them
	// disagree with URL.
	mediaType    string
	decodedBytes int
}

// MediaType is the validated media type, e.g. "image/png".
func (i *ImageURL) MediaType() string { return i.mediaType }

// DecodedBytes is the size of the image after base64 decoding.
func (i *ImageURL) DecodedBytes() int { return i.decodedBytes }

// Base64Data is the base64 payload of the data URL, without its prefix. The
// Anthropic adapter carries it in a base64 image source block.
func (i *ImageURL) Base64Data() string {
	_, data, _ := strings.Cut(i.URL, ",")
	return data
}

// imageDetails are the values OpenAI defines for image_url.detail.
var imageDetails = map[string]bool{"": true, "auto": true, "low": true, "high": true}

// imageMagic maps each admitted media type to the leading bytes its encoding
// must start with. The declared type is checked against the bytes, not taken on
// trust, because the media type is sealed into the audit trail and a sealed
// value has to be one the gateway verified.
var imageMagic = map[string][][]byte{
	"image/png":  {[]byte("\x89PNG\r\n\x1a\n")},
	"image/jpeg": {[]byte{0xFF, 0xD8, 0xFF}},
	"image/gif":  {[]byte("GIF87a"), []byte("GIF89a")},
	"image/webp": {[]byte("RIFF")}, // and "WEBP" at offset 8, checked below
}

// ErrNonTextContentPart is returned when a content array carries a part whose
// type AEGIS does not accept. It is a named error so the HTTP layer can turn it
// into a specific 400 rather than a generic JSON parse failure.
var ErrNonTextContentPart = errors.New("unsupported content part")

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
	return fmt.Sprintf("content[%d] has type %q: AEGIS accepts only text and image_url content parts. "+
		"It cannot scan any other kind for secrets, PII or prompt injection, "+
		"and an unscannable part would leave the gateway for the provider unfiltered", e.Index, t)
}

func (e *NonTextPartError) Unwrap() error { return ErrNonTextContentPart }

// ErrInvalidImagePart is returned for an image_url part AEGIS will not carry.
var ErrInvalidImagePart = errors.New("invalid image content part")

// ImagePartError reports why an image part was refused. Reason never quotes the
// URL: it can be megabytes of base64, and a remote URL is caller text that may
// carry a credential in its query string.
type ImagePartError struct {
	Index  int
	Reason string
}

func (e *ImagePartError) Error() string {
	return fmt.Sprintf("content[%d] is an image_url part AEGIS will not carry: %s", e.Index, e.Reason)
}

func (e *ImagePartError) Unwrap() error { return ErrInvalidImagePart }

// parseImageDataURL validates a data URL and returns its media type and decoded
// size. It accepts only data:<allowlisted type>;base64,<payload>.
func parseImageDataURL(u string) (string, int, string) {
	rest, ok := strings.CutPrefix(u, "data:")
	if !ok {
		return "", 0, "only base64 data: URLs are accepted. A remote image URL is refused because the " +
			"provider would fetch content the gateway never sees"
	}
	header, payload, ok := strings.Cut(rest, ",")
	if !ok {
		return "", 0, "malformed data URL: no comma separating the header from the data"
	}
	mediaType, ok := strings.CutSuffix(header, ";base64")
	if !ok {
		return "", 0, "data URL must be base64-encoded (data:<type>;base64,...)"
	}
	mediaType = strings.ToLower(mediaType)
	magics, ok := imageMagic[mediaType]
	if !ok {
		return "", 0, "media type is not one of image/png, image/jpeg, image/gif, image/webp"
	}
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return "", 0, "data URL payload is not valid base64"
	}
	matched := false
	for _, m := range magics {
		if bytes.HasPrefix(raw, m) {
			matched = true
			break
		}
	}
	if mediaType == "image/webp" && (len(raw) < 12 || string(raw[8:12]) != "WEBP") {
		matched = false
	}
	if !matched {
		return "", 0, "image bytes do not match the declared media type " + mediaType
	}
	return mediaType, len(raw), ""
}

// MarshalJSON emits the OpenAI wire shape for the part's type.
func (p ContentPart) MarshalJSON() ([]byte, error) {
	switch p.Type {
	case PartImageURL:
		if p.ImageURL == nil {
			return nil, errors.New("image_url part has no image_url")
		}
		return json.Marshal(struct {
			Type     ContentPartType `json:"type"`
			ImageURL *ImageURL       `json:"image_url"`
		}{p.Type, p.ImageURL})
	default:
		return json.Marshal(struct {
			Type ContentPartType `json:"type"`
			Text string          `json:"text"`
		}{p.Type, p.Text})
	}
}

// MarshalJSON emits {"url":...,"detail":...}, omitting an absent detail so the
// part leaves in the shape it arrived.
func (i ImageURL) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		URL    string `json:"url"`
		Detail string `json:"detail,omitempty"`
	}{i.URL, i.Detail})
}

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
		switch ContentPartType(probe.Type) {
		case PartText:
			var part struct {
				Type ContentPartType `json:"type"`
				Text string          `json:"text"`
			}
			if err := decodeStrict(elem, &part); err != nil {
				return fmt.Errorf("content[%d]: %w", i, err)
			}
			parts = append(parts, ContentPart{Type: PartText, Text: part.Text})

		case PartImageURL:
			var part struct {
				Type     ContentPartType `json:"type"`
				ImageURL *struct {
					URL    string `json:"url"`
					Detail string `json:"detail"`
				} `json:"image_url"`
			}
			if err := decodeStrict(elem, &part); err != nil {
				return fmt.Errorf("content[%d]: %w", i, err)
			}
			if part.ImageURL == nil {
				return &ImagePartError{Index: i, Reason: "missing image_url object"}
			}
			if !imageDetails[part.ImageURL.Detail] {
				return &ImagePartError{Index: i, Reason: `detail must be "auto", "low" or "high"`}
			}
			mediaType, n, reason := parseImageDataURL(part.ImageURL.URL)
			if reason != "" {
				return &ImagePartError{Index: i, Reason: reason}
			}
			parts = append(parts, ContentPart{Type: PartImageURL, ImageURL: &ImageURL{
				URL: part.ImageURL.URL, Detail: part.ImageURL.Detail,
				mediaType: mediaType, decodedBytes: n,
			}})

		default:
			return &NonTextPartError{Index: i, Type: probe.Type}
		}
	}

	*c = Content{Kind: ContentParts, Parts: parts}
	return nil
}

func decodeStrict(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// ImageParts returns the content's image parts, in order.
func (c Content) ImageParts() []*ImageURL {
	if c.Kind != ContentParts {
		return nil
	}
	var out []*ImageURL
	for _, p := range c.Parts {
		if p.Type == PartImageURL && p.ImageURL != nil {
			out = append(out, p.ImageURL)
		}
	}
	return out
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
