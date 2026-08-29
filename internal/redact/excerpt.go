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

// Package redact bounds untrusted third-party text before it reaches a log.
package redact

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ExcerptLimit is the maximum number of runes of provider text that may reach a
// log record.
//
// Small on purpose. The value of a provider error body to an operator is
// concentrated in its first line, which carries the error type and code; the
// rest is the provider restating the request, which is where caller content
// gets echoed back. 256 keeps a typical OpenAI or Anthropic error envelope
// legible while making it impossible for a long body to land in the log whole.
const ExcerptLimit = 256

// Excerpt renders an untrusted body as a bounded single-line string safe to put
// in a log record.
//
// Provider error bodies are unbounded strings the gateway does not control, and
// they routinely echo the caller's own content back. Logging one verbatim puts
// caller-supplied text into whatever collects the process logs, which is
// exactly the durable copy the product claims not to make. That claim is about
// prompts and responses reaching storage, and a log shipper is storage.
//
// This does not attempt to detect secrets or PII inside the excerpt and must
// not be described as though it does. It bounds the volume, forces one line so
// a body cannot forge additional log records, and drops control characters. The
// residual 256 characters are still provider-controlled text.
func Excerpt(body []byte) string {
	if len(body) == 0 {
		return "(empty body)"
	}

	// Collapse to one line first. A body containing newlines would otherwise
	// span log records, and in a line-oriented collector a crafted body can
	// impersonate an entirely separate event.
	var b strings.Builder
	b.Grow(min(len(body), ExcerptLimit))

	runes := 0
	lastWasSpace := false
	truncated := false

	for i := 0; i < len(body); {
		r, size := utf8.DecodeRune(body[i:])
		i += size

		// DecodeRune yields RuneError with size 1 for invalid input. Passing
		// that through would emit U+FFFD for every stray byte; one marker for
		// the run is enough.
		if r == utf8.RuneError && size == 1 {
			r = '?'
		}

		switch {
		case unicode.IsSpace(r):
			if lastWasSpace || runes == 0 {
				continue
			}
			r = ' '
			lastWasSpace = true
		case unicode.IsControl(r):
			continue
		default:
			lastWasSpace = false
		}

		if runes >= ExcerptLimit {
			truncated = true
			break
		}
		b.WriteRune(r)
		runes++
	}

	out := strings.TrimRight(b.String(), " ")
	if out == "" {
		return "(no printable content, " + strconv.Itoa(len(body)) + " bytes)"
	}
	if truncated {
		// The original length is the operationally useful part of knowing it
		// was cut: it separates "the provider was terse" from "the provider
		// returned a megabyte".
		return out + "... (truncated, " + strconv.Itoa(len(body)) + " bytes total)"
	}
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
