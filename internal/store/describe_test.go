package store_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/otfabric/lan-tracker/internal/store"
)

func TestCleanDescription(t *testing.T) {
	for _, tt := range []struct {
		in, want string
		ok       bool
	}{
		{"  Solar panel rooftop  ", "Solar panel rooftop", true},
		{"", "", true},
		{"   ", "", true},
		{"Zonnepanelen dak — kantoor", "Zonnepanelen dak — kantoor", true},
		{strings.Repeat("é", store.MaxDescription), strings.Repeat("é", store.MaxDescription), true}, // characters, not bytes
		{strings.Repeat("x", store.MaxDescription+1), "", false},
		{"two\nlines", "", false},
		{"a\ttab", "", false},
		{"bad \xff byte", "", false},
		{"pump \u202eA tes\u202c", "", false},
		{"pump \u2066B\u2069", "", false},
		{"pump \u200fC", "", false},
		{"family \U0001F468\u200d\U0001F469", "family \U0001F468\u200d\U0001F469", true}, // a zero-width joiner is not a bidi control
	} {
		got, err := store.CleanDescription(tt.in)
		if got != tt.want || (err == nil) != tt.ok || err != nil && !errors.Is(err, store.ErrDescription) {
			t.Errorf("CleanDescription(%q) = %q, %v", tt.in, got, err)
		}
	}
}
