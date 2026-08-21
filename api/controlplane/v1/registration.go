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

package controlplanev1

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// MaxNameLen bounds every operator-supplied label in this protocol.
//
// The bound exists so that a label cannot become a smuggling channel for
// content. A field that accepts unbounded text is a field into which a prompt
// fits.
const MaxNameLen = 200

// MaxVersionLen bounds build version strings.
const MaxVersionLen = 64

// GatewayRegistration announces one gateway deployment to a control plane.
//
// The caller is identified by its bearer token, which resolves to exactly one
// organization. A registration therefore names no organization: a gateway
// cannot register itself into an organization other than its token's.
type GatewayRegistration struct {
	// Name is an operator-chosen label distinguishing this deployment from
	// others in the same organization, for example "eu-west-prod". It is
	// supplied by whoever configured the gateway and is never derived from a
	// governed request.
	//
	// Registration is idempotent on this value: a gateway that restarts and
	// registers again under the same name receives the same gateway ID.
	Name string `json:"name"`

	// GatewayVersion is the build version of the registering gateway, as
	// reported by its version ldflag.
	GatewayVersion string `json:"gateway_version"`
}

// Validate checks the registration against the wire contract.
func (r *GatewayRegistration) Validate() error {
	if err := validateLabel("name", r.Name, MaxNameLen); err != nil {
		return err
	}
	return validateLabel("gateway_version", r.GatewayVersion, MaxVersionLen)
}

// GatewayRegistrationResponse is returned for an accepted registration.
type GatewayRegistrationResponse struct {
	// GatewayID identifies this deployment in every later checkpoint
	// submission. It is assigned by the control plane.
	GatewayID string `json:"gateway_id"`

	// OrgID is the organization the bearer token resolved to. It is echoed so
	// an operator can confirm which tenant a token belongs to without a
	// separate call.
	OrgID string `json:"org_id"`

	// Name is the registered label, echoed.
	Name string `json:"name"`

	// RegisteredAt is when this gateway was first registered. On a repeat
	// registration it is the original time, not the time of the repeat, so it
	// is visible that the registration was idempotent rather than new.
	RegisteredAt Timestamp `json:"registered_at"`
}

// validateLabel enforces the shared rules for short operator-supplied strings:
// present, valid UTF-8, within the length bound, and free of control
// characters. Control characters are rejected because a label ends up in log
// lines and in exported evidence, where an embedded newline or escape sequence
// can misrepresent surrounding content.
func validateLabel(field, value string, maxLen int) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", field)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s is not valid UTF-8", field)
	}
	if utf8.RuneCountInString(value) > maxLen {
		return fmt.Errorf("%s exceeds %d characters", field, maxLen)
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%s contains a control character", field)
		}
	}
	return nil
}
