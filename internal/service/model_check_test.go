package service

import (
	"testing"

	"tgtriage/internal/domain"
)

func TestCheckModelOpenRouterFreeOnly(t *testing.T) {
	for model, ok := range map[string]bool{
		"openrouter/free":                        true,
		"google/gemma-4-31b-it:free":             true,
		"anthropic/claude-sonnet-5":              false,
		"openai/gpt-5:free-not":                  false,
		"nvidia/nemotron-3-super-120b-a12b:free": true,
	} {
		if got := CheckModel(domain.ProviderOpenRouter, model) == nil; got != ok {
			t.Errorf("openrouter %q: allowed=%v, want %v", model, got, ok)
		}
	}
	if err := CheckModel(domain.ProviderGroq, "openai/gpt-oss-120b"); err != nil {
		t.Errorf("other providers must not be restricted: %v", err)
	}
}
