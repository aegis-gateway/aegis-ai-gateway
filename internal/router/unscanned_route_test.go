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

package router

import (
	"testing"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/config"
)

var imageKinds = []string{"image"}

// Default deny: a route that does not opt in never carries an image, and a
// text-only request is unaffected by the new field.
func TestResolveRouteFor_DefaultDeniesUnscanned(t *testing.T) {
	cfg := modelsCfgWith(map[string]config.ModelMapping{
		"m": {Primary: config.ProviderRoute{Provider: "cloud", Model: "x"}},
	})
	reg := newTestRegistry("cloud")

	if _, err := ResolveRouteFor(cfg, reg, nil, "m", "INTERNAL", imageKinds); err == nil {
		t.Error("an image request routed to a route with no unscanned_parts opt-in")
	}
	if _, err := ResolveRouteFor(cfg, reg, nil, "m", "INTERNAL", nil); err != nil {
		t.Errorf("text-only request refused: %v", err)
	}
	if AnyRouteAdmitsUnscanned(cfg, "m", imageKinds) {
		t.Error("AnyRouteAdmitsUnscanned true with no opt-in")
	}
}

func TestResolveRouteFor_OptedInRouteCarriesImages(t *testing.T) {
	cfg := modelsCfgWith(map[string]config.ModelMapping{
		"local": {Primary: config.ProviderRoute{
			Provider: "jetson", Model: "qwen3-vl", ClassificationCeiling: "RESTRICTED",
			UnscannedParts: []string{"image"},
		}},
	})
	route, err := ResolveRouteFor(cfg, newTestRegistry("jetson"), nil, "local", "RESTRICTED", imageKinds)
	if err != nil || route.ProviderKey != "jetson" {
		t.Fatalf("got %+v, %v", route, err)
	}
}

// The case the per-route check exists for: the cleared primary is down, and
// the fallback was never cleared. The request must fail, not fail over.
func TestResolveRouteFor_FallbackMustOptInSeparately(t *testing.T) {
	cfg := modelsCfgWith(map[string]config.ModelMapping{
		"m": {
			Primary: config.ProviderRoute{Provider: "local", Model: "vl", UnscannedParts: []string{"image"}},
			Fallback: []config.ProviderRoute{
				{Provider: "cloud", Model: "x"},
			},
		},
	})
	// "local" is configured but not registered, which ResolveRoute treats
	// like an unavailable provider.
	reg := newTestRegistry("cloud")

	if route, err := ResolveRouteFor(cfg, reg, nil, "m", "INTERNAL", imageKinds); err == nil {
		t.Fatalf("image request failed over to uncleared fallback %q", route.ProviderKey)
	}
	if !AnyRouteAdmitsUnscanned(cfg, "m", imageKinds) {
		t.Error("AnyRouteAdmitsUnscanned false although the primary opts in; the handler would report " +
			"a permanent refusal for what is an outage")
	}
	// Text still fails over as before.
	if route, err := ResolveRouteFor(cfg, reg, nil, "m", "INTERNAL", nil); err != nil || route.ProviderKey != "cloud" {
		t.Errorf("text request: got %+v, %v", route, err)
	}
}

// A cleared fallback is used when the primary is not cleared.
func TestResolveRouteFor_SkipsUnclearedPrimary(t *testing.T) {
	cfg := modelsCfgWith(map[string]config.ModelMapping{
		"m": {
			Primary:  config.ProviderRoute{Provider: "cloud", Model: "x"},
			Fallback: []config.ProviderRoute{{Provider: "local", Model: "vl", UnscannedParts: []string{"image"}}},
		},
	})
	route, err := ResolveRouteFor(cfg, newTestRegistry("cloud", "local"), nil, "m", "INTERNAL", imageKinds)
	if err != nil || route.ProviderKey != "local" {
		t.Errorf("got %+v, %v", route, err)
	}
}

// The ceiling still applies to a cleared route.
func TestResolveRouteFor_CeilingStillApplies(t *testing.T) {
	cfg := modelsCfgWith(map[string]config.ModelMapping{
		"m": {Primary: config.ProviderRoute{
			Provider: "local", Model: "vl", ClassificationCeiling: "INTERNAL", UnscannedParts: []string{"image"},
		}},
	})
	if _, err := ResolveRouteFor(cfg, newTestRegistry("local"), nil, "m", "RESTRICTED", imageKinds); err == nil {
		t.Error("image opt-in bypassed the classification ceiling")
	}
}

// A typo in the opt-in admits nothing.
func TestAdmitsUnscanned_UnknownKindFailsClosed(t *testing.T) {
	r := config.ProviderRoute{UnscannedParts: []string{"images", "IMAGE"}}
	if r.AdmitsUnscanned(imageKinds) {
		t.Error("a misspelled kind admitted images")
	}
	if !r.AdmitsUnscanned(nil) {
		t.Error("a route must admit a request with no unscanned parts")
	}
}
