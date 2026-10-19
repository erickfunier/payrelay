package domain

import (
	"strings"
	"testing"
)

func TestNewID_GivenPrefix_WhenGenerated_ThenIncludesPrefixAndRandomValue(t *testing.T) {
	first := NewID("evt")
	second := NewID("evt")
	if !strings.HasPrefix(first, "evt_") || len(first) != len("evt_")+24 {
		t.Fatalf("unexpected ID format: %q", first)
	}
	if first == second {
		t.Fatal("generated duplicate IDs")
	}
}
