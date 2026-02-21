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
