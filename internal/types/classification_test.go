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

import "testing"

func TestClassificationLevel(t *testing.T) {
	tests := []struct {
		c     Classification
		level int
	}{
		{ClassPublic, 0},
		{ClassInternal, 1},
		{ClassConfidential, 2},
		{ClassRestricted, 3},
		{Classification("INVALID"), -1},
	}

	for _, tt := range tests {
		if got := tt.c.Level(); got != tt.level {
			t.Errorf("%s.Level() = %d, want %d", tt.c, got, tt.level)
		}
	}
}

func TestClassificationAllows(t *testing.T) {
	tests := []struct {
		holder Classification
		data   Classification
		allows bool
	}{
		{ClassRestricted, ClassPublic, true},
		{ClassRestricted, ClassRestricted, true},
		{ClassConfidential, ClassRestricted, false},
		{ClassPublic, ClassInternal, false},
		{ClassInternal, ClassInternal, true},
	}

	for _, tt := range tests {
		if got := tt.holder.Allows(tt.data); got != tt.allows {
			t.Errorf("%s.Allows(%s) = %v, want %v", tt.holder, tt.data, got, tt.allows)
		}
	}
}

func TestParseClassification(t *testing.T) {
	tests := []struct {
		input string
		valid bool
	}{
		{"PUBLIC", true},
		{"INTERNAL", true},
		{"CONFIDENTIAL", true},
		{"RESTRICTED", true},
		{"invalid", false},
		{"", false},
	}

	for _, tt := range tests {
		_, ok := ParseClassification(tt.input)
		if ok != tt.valid {
			t.Errorf("ParseClassification(%q) valid = %v, want %v", tt.input, ok, tt.valid)
		}
	}
}
