package registry

import "testing"

// GetClaudeModels is the package's only exported Claude accessor on purpose.
// When the latest-generation ids lived behind a second exported accessor, every
// call site had to remember which one was current.
func TestClaudeAccessorCarriesLatestGeneration(t *testing.T) {
	all := make(map[string]bool)
	for _, model := range GetClaudeModels() {
		if model == nil {
			t.Fatal("GetClaudeModels returned a nil entry")
		}
		all[model.ID] = true
	}

	latest := latestClaudeModels()
	if len(latest) == 0 {
		t.Fatal("latestClaudeModels is empty; this test would prove nothing")
	}
	for _, model := range latest {
		if !all[model.ID] {
			t.Errorf("GetClaudeModels is missing latest-generation id %q", model.ID)
		}
	}

	for _, model := range claudeStaticSnapshot() {
		if !all[model.ID] {
			t.Errorf("GetClaudeModels is missing static snapshot id %q", model.ID)
		}
	}
}

// The claude channel serves the full list, latest generation included.
func TestClaudeChannelCarriesLatestGeneration(t *testing.T) {
	channelIDs := make(map[string]bool)
	for _, model := range GetStaticModelDefinitionsByChannel("claude") {
		channelIDs[model.ID] = true
	}
	for _, model := range latestClaudeModels() {
		if !channelIDs[model.ID] {
			t.Errorf("claude channel is missing latest-generation id %q", model.ID)
		}
	}
}

// The latest-generation ids are served by Anthropic's Claude Code OAuth surface
// only. Bedrock does not expose them, so the Bedrock list is built from the
// static snapshot and must not advertise models the channel cannot serve.
func TestBedrockDoesNotAdvertiseOAuthOnlyClaudeModels(t *testing.T) {
	latestIDs := make(map[string]bool)
	for _, model := range latestClaudeModels() {
		latestIDs[model.ID] = true
	}
	snapshotIDs := make(map[string]bool)
	for _, model := range claudeStaticSnapshot() {
		snapshotIDs[model.ID] = true
	}

	for _, tc := range []struct {
		name   string
		models []*ModelInfo
	}{
		{"bedrock", GetBedrockModels()},
		{"channel:bedrock", GetStaticModelDefinitionsByChannel("bedrock")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.models) != len(snapshotIDs) {
				t.Errorf("%s exposes %d models, static snapshot has %d", tc.name, len(tc.models), len(snapshotIDs))
			}
			for _, model := range tc.models {
				if latestIDs[model.ID] {
					t.Errorf("%s advertises %q, which Bedrock does not serve", tc.name, model.ID)
				}
				if model.Type != "bedrock" || model.OwnedBy != "aws" {
					t.Errorf("%s entry %q has type=%q owned_by=%q, want bedrock/aws", tc.name, model.ID, model.Type, model.OwnedBy)
				}
			}
		})
	}
}

// LookupStaticModelInfo has to resolve the latest ids, otherwise a request for
// one of them falls back to conservative defaults for context and thinking.
func TestLookupStaticModelInfoResolvesLatestClaude(t *testing.T) {
	for _, model := range latestClaudeModels() {
		info := LookupStaticModelInfo(model.ID)
		if info == nil {
			t.Errorf("LookupStaticModelInfo(%q) = nil", model.ID)
			continue
		}
		if info.ContextLength != model.ContextLength {
			t.Errorf("LookupStaticModelInfo(%q).ContextLength = %d, want %d",
				model.ID, info.ContextLength, model.ContextLength)
		}
	}
}
