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
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func imageBody(parts string) []byte {
	return []byte(`{"model":"m","messages":[{"role":"user","content":[` + parts + `]}]}`)
}

func dataURL(mediaType string, raw []byte) string {
	return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(raw)
}

func TestDecode_ImagePartAccepted(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"png":  testPNGDataURL,
		"jpeg": dataURL("image/jpeg", []byte{0xFF, 0xD8, 0xFF, 0xE0, 1, 2, 3}),
		"gif":  dataURL("image/gif", []byte("GIF89a\x01\x00")),
		"webp": dataURL("image/webp", []byte("RIFF\x00\x00\x00\x00WEBPVP8 ")),
	}
	for name, url := range cases {
		req, err := DecodeChatCompletion(imageBody(
			`{"type":"text","text":"what is this"},{"type":"image_url","image_url":{"url":"` + url + `","detail":"low"}}`))
		if err != nil {
			t.Errorf("%s: refused: %v", name, err)
			continue
		}
		imgs := req.Messages[0].Content.ImageParts()
		if len(imgs) != 1 {
			t.Fatalf("%s: got %d image parts, want 1", name, len(imgs))
		}
		if !strings.HasPrefix(imgs[0].MediaType(), "image/") || imgs[0].DecodedBytes() == 0 {
			t.Errorf("%s: media type %q, %d bytes", name, imgs[0].MediaType(), imgs[0].DecodedBytes())
		}
	}
}

// The image part has to leave for the provider in the shape it arrived, and the
// text beside it has to stay scanned.
func TestDecode_ImagePartRoundTripsAndTextStaysScanned(t *testing.T) {
	t.Parallel()

	in := `{"type":"text","text":"SENTINEL_beside_image"},{"type":"image_url","image_url":{"url":"` + testPNGDataURL + `"}}`
	req, err := DecodeChatCompletion(imageBody(in))
	if err != nil {
		t.Fatal(err)
	}

	out, err := json.Marshal(req.Messages[0].Content)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "["+in+"]" {
		t.Errorf("content did not round-trip:\n got %s\nwant [%s]", out, in)
	}

	var found bool
	for _, seg := range req.TextSegments() {
		if seg.Text == "SENTINEL_beside_image" {
			found = true
		}
		if strings.Contains(seg.Text, "iVBOR") {
			t.Errorf("image data reached TextSegments: filters would scan base64 as text")
		}
	}
	if !found {
		t.Error("the text part beside an image is no longer scanned")
	}
	if got := req.Messages[0].Content.Flatten(); got != "SENTINEL_beside_image" {
		t.Errorf("Flatten = %q", got)
	}
}

func TestDecode_ImagePartRefusals(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"remote https":       `{"type":"image_url","image_url":{"url":"https://example.invalid/a.png?token=sk-secret"}}`,
		"missing image_url":  `{"type":"image_url"}`,
		"bad detail":         `{"type":"image_url","image_url":{"url":"` + testPNGDataURL + `","detail":"ultra"}}`,
		"unknown field":      `{"type":"image_url","image_url":{"url":"` + testPNGDataURL + `","extra":"x"}}`,
		"unknown part field": `{"type":"image_url","image_url":{"url":"` + testPNGDataURL + `"},"text":"smuggled"}`,
		"type mismatch":      `{"type":"image_url","image_url":{"url":"` + dataURL("image/jpeg", []byte("\x89PNG\r\n\x1a\n")) + `"}}`,
		"webp without WEBP":  `{"type":"image_url","image_url":{"url":"` + dataURL("image/webp", []byte("RIFF\x00\x00\x00\x00AVI ")) + `"}}`,
		"bad base64":         `{"type":"image_url","image_url":{"url":"data:image/png;base64,!!!"}}`,
		"svg":                `{"type":"image_url","image_url":{"url":"` + dataURL("image/svg+xml", []byte("<svg/>")) + `"}}`,
		"text part with url": `{"type":"text","text":"x","image_url":{"url":"` + testPNGDataURL + `"}}`,
	}
	for name, part := range cases {
		_, err := DecodeChatCompletion(imageBody(part))
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		// The refusal must never echo the URL: a remote URL can carry a
		// credential, and a data URL can be megabytes.
		if strings.Contains(err.Error(), "sk-secret") || strings.Contains(err.Error(), "iVBOR") {
			t.Errorf("%s: error echoes the URL: %v", name, err)
		}
	}

	_, err := DecodeChatCompletion(imageBody(`{"type":"image_url","image_url":{"url":"https://example.invalid/a.png"}}`))
	var ipe *ImagePartError
	if !errors.As(err, &ipe) || !errors.Is(err, ErrInvalidImagePart) || ipe.Index != 0 {
		t.Errorf("remote URL: got %v, want *ImagePartError at index 0", err)
	}
}

func TestUnscannedParts_Summary(t *testing.T) {
	t.Parallel()

	jpeg := dataURL("image/jpeg", []byte{0xFF, 0xD8, 0xFF, 0xE0, 1, 2, 3})
	body := []byte(`{"model":"m","messages":[
		{"role":"user","content":[{"type":"image_url","image_url":{"url":"` + jpeg + `"}},{"type":"image_url","image_url":{"url":"` + testPNGDataURL + `"}}]},
		{"role":"assistant","content":"ok"},
		{"role":"user","content":[{"type":"image_url","image_url":{"url":"` + jpeg + `"}}]}
	]}`)
	req, err := DecodeChatCompletion(body)
	if err != nil {
		t.Fatal(err)
	}
	s := req.UnscannedParts()
	if s.Parts != 3 || s.Bytes != 7+7+70 {
		t.Errorf("parts=%d bytes=%d", s.Parts, s.Bytes)
	}
	if strings.Join(s.MediaTypes, ",") != "image/jpeg,image/png" {
		t.Errorf("media types %v, want sorted and de-duplicated", s.MediaTypes)
	}

	plain, _ := DecodeChatCompletion([]byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
	if !plain.UnscannedParts().Empty() {
		t.Error("a text-only request reports unscanned parts")
	}
}
