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

package adapters

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/filter/secrets"
)

// MaxProviderErrorExcerpt bounds how much of a provider error body reaches a
// log line, in bytes.
//
// 256 is enough to carry the shape of a provider error envelope, which is what
// an operator actually needs: the type and code fields of an OpenAI or
// Anthropic error object sit in the first hundred or so bytes. It is far too
// small to carry a conversation.
//
// The bound is on the excerpt, not on the read. Callers already read the body
// to decide what to do with it; this governs only what is repeated.
const MaxProviderErrorExcerpt = 256

// errorBodyScanner is the secret detector applied to provider error bodies.
// Package level because DefaultPatterns compiles regexes and an error path
// should not pay for that per request.
var errorBodyScanner = secrets.NewScanner()

// RedactProviderError renders a provider's error response body as a bounded,
// redacted, single-line excerpt safe to put in a log record.
//
// Three things happen to the body, in this order, and the order matters.
//
//  1. Secret spans are replaced. A provider that rejects a request commonly
//     quotes part of it back, and the part most worth quoting back is often the
//     credential that failed. The gateway already refuses to forward such a
//     request on the way out; repeating it on the way back would put it in the
//     logs anyway, by a different route.
//
//  2. Control characters are collapsed to spaces. A body is attacker
//     influenced text arriving at a JSON log handler, and an embedded newline
//     is how one log record is made to look like two.
//
//  3. The result is truncated to MaxProviderErrorExcerpt bytes on a rune
//     boundary, with the full length stated so a reader knows what they are not
//     seeing.
//
// Redaction runs before truncation so that a secret beginning inside the
// retained prefix and ending past it is still replaced rather than clipped in
// half and emitted.
//
// This is a reduction in what is disclosed, not a guarantee about it. A
// provider error body is text the gateway does not control, and a 256 byte
// excerpt of it may still contain caller-derived words that no pattern matches.
// It is bounded, it cannot carry a payload, and it cannot forge a log record.
// Anything stronger would mean not logging the body at all, which costs the
// ability to diagnose a provider rejection from the logs.
func RedactProviderError(body []byte) string {
	if len(body) == 0 {
		return "<empty body>"
	}

	redacted := redactSecrets(string(body))
	redacted = collapseControlChars(redacted)

	if len(redacted) <= MaxProviderErrorExcerpt {
		return redacted
	}

	cut := MaxProviderErrorExcerpt
	for cut > 0 && !utf8.RuneStart(redacted[cut]) {
		cut--
	}
	return fmt.Sprintf("%s [truncated, %d bytes total]", redacted[:cut], len(body))
}

// redactSecrets replaces every detected secret span with a marker naming the
// pattern that matched. Overlapping detections are merged so a span is not
// rewritten twice.
func redactSecrets(s string) string {
	detections := errorBodyScanner.Scan(s)
	if len(detections) == 0 {
		return s
	}

	sort.Slice(detections, func(i, j int) bool {
		if detections[i].Start != detections[j].Start {
			return detections[i].Start < detections[j].Start
		}
		return detections[i].End > detections[j].End
	})

	var b strings.Builder
	prev := 0
	for _, d := range detections {
		if d.Start < prev {
			// Overlaps a span already replaced. The wider one wins, and the
			// wider one sorts first, so this is contained and can be skipped.
			continue
		}
		if d.Start > len(s) || d.End > len(s) || d.Start > d.End {
			continue
		}
		b.WriteString(s[prev:d.Start])
		b.WriteString("[redacted:")
		b.WriteString(d.PatternName)
		b.WriteString("]")
		prev = d.End
	}
	b.WriteString(s[prev:])
	return b.String()
}

// collapseControlChars replaces every C0 control character and DEL with a
// space, so an excerpt cannot end a log record and begin a forged one.
func collapseControlChars(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}
