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

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeModelsYAML(t *testing.T, aliases ...string) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("models:\n")
	for _, a := range aliases {
		b.WriteString("  " + a + ":\n    display_name: x\n")
	}
	path := filepath.Join(t.TempDir(), "models.yaml")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	return path
}

// "any" is the only way to ask for an unrestricted key, and it yields the empty
// array the gateway reads as no restriction. Everything else must name models.
func TestParseAllowedModels_AnyIsExplicitAndUnrestricted(t *testing.T) {
	cfg := writeModelsYAML(t, "aegis-fast")

	for _, in := range []string{"any", "ANY", " any "} {
		got, unrestricted, err := parseAllowedModels(in, cfg)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if !unrestricted {
			t.Errorf("%q did not report an unrestricted key", in)
		}
		if len(got) != 0 {
			t.Errorf("%q produced %v, want an empty array, which is the value the gateway "+
				"reads as no restriction", in, got)
		}
	}
}

func TestParseAllowedModels_TrimsAndDeduplicates(t *testing.T) {
	cfg := writeModelsYAML(t, "aegis-fast", "aegis-balanced")

	got, unrestricted, err := parseAllowedModels(" aegis-fast , aegis-balanced ,aegis-fast", cfg)
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	if unrestricted {
		t.Error("a named list reported itself as unrestricted")
	}
	if len(got) != 2 || got[0] != "aegis-fast" || got[1] != "aegis-balanced" {
		t.Errorf("got %v, want [aegis-fast aegis-balanced] in first-seen order", got)
	}
}

// A misspelled alias is otherwise a key that authenticates and is refused every
// model, because matching is exact string equality, and the operator learns it
// from a caller rather than from the tool.
func TestParseAllowedModels_RejectsAnUnconfiguredAlias(t *testing.T) {
	cfg := writeModelsYAML(t, "aegis-fast", "aegis-balanced")

	_, _, err := parseAllowedModels("aegis-fastt", cfg)
	if err == nil {
		t.Fatal("an alias that is not configured was accepted")
	}
	if !strings.Contains(err.Error(), "aegis-fastt") {
		t.Errorf("error does not name the offending alias: %v", err)
	}
	// The operator needs to know what the real ones are.
	for _, want := range []string{"aegis-fast", "aegis-balanced"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not list the configured alias %q: %v", want, err)
		}
	}
}

func TestParseAllowedModels_RejectsAnEmptyEntry(t *testing.T) {
	cfg := writeModelsYAML(t, "aegis-fast")

	if _, _, err := parseAllowedModels("aegis-fast,,", cfg); err == nil {
		t.Error("an empty list entry was accepted")
	}
}

// keygen must still work where models.yaml is not on disk, for instance run
// against a remote database from elsewhere. It degrades to the previous
// behaviour and says so, rather than refusing to issue a key.
func TestParseAllowedModels_UnreadableConfigSkipsTheCheck(t *testing.T) {
	got, unrestricted, err := parseAllowedModels("whatever-alias", "/nonexistent/models.yaml")
	if err != nil {
		t.Fatalf("an unreadable config must not fail the key: %v", err)
	}
	if unrestricted {
		t.Error("a named list reported itself as unrestricted")
	}
	if len(got) != 1 || got[0] != "whatever-alias" {
		t.Errorf("got %v, want the names through unchecked", got)
	}
}
