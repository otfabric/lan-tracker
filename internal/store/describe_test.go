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
	} {
		got, err := store.CleanDescription(tt.in)
		if got != tt.want || (err == nil) != tt.ok || err != nil && !errors.Is(err, store.ErrDescription) {
			t.Errorf("CleanDescription(%q) = %q, %v", tt.in, got, err)
		}
	}
}
