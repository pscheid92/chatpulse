package httpserver

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Unit tests for validateConfig (no external dependencies)

func TestValidateConfig_EmptyTriggerFor(t *testing.T) {
	err := validateConfig("  ", "Right", "no", "Left", 30, "combined", "dark")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "for trigger cannot be empty")
}

func TestValidateConfig_WhitespaceOnlyTrigger(t *testing.T) {
	// Edge case: 500 spaces should be rejected (becomes empty after trim)
	// This tests the issue where len() check happens before trim
	spaces := make([]byte, 500)
	for i := range spaces {
		spaces[i] = ' '
	}
	err := validateConfig(string(spaces), "Right", "no", "Left", 30, "combined", "dark")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "for trigger cannot be empty")
}

func TestValidateConfig_EmptyTriggerAgainst(t *testing.T) {
	err := validateConfig("yes", "Right", "  ", "Left", 30, "combined", "dark")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "against trigger cannot be empty")
}

func TestValidateConfig_ForTriggerTooLong(t *testing.T) {
	longTrigger := string(make([]byte, 501)) // 501 characters
	err := validateConfig(longTrigger, "Right", "no", "Left", 30, "combined", "dark")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "for trigger exceeds 500 characters")
}

func TestValidateConfig_AgainstTriggerTooLong(t *testing.T) {
	longTrigger := string(make([]byte, 501)) // 501 characters
	err := validateConfig("yes", "Right", longTrigger, "Left", 30, "combined", "dark")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "against trigger exceeds 500 characters")
}

func TestValidateConfig_AgainstLabelTooLong(t *testing.T) {
	longLabel := string(make([]byte, 51)) // 51 characters
	err := validateConfig("yes", "Right", "no", longLabel, 30, "combined", "dark")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "against label exceeds 50 characters")
}

func TestValidateConfig_ForLabelTooLong(t *testing.T) {
	longLabel := string(make([]byte, 51)) // 51 characters
	err := validateConfig("yes", longLabel, "no", "Left", 30, "combined", "dark")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "for label exceeds 50 characters")
}

func TestValidateConfig_MemorySecondsTooLow(t *testing.T) {
	err := validateConfig("yes", "Right", "no", "Left", 4, "combined", "dark")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "memory seconds must be between")
}

func TestValidateConfig_MemorySecondsTooHigh(t *testing.T) {
	err := validateConfig("yes", "Right", "no", "Left", 121, "combined", "dark")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "memory seconds must be between")
}

func TestValidateConfig_IdenticalTriggers(t *testing.T) {
	err := validateConfig("PogChamp", "Right", "pogchamp", "Left", 30, "combined", "dark")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "for trigger and against trigger must be different")
}

func TestValidateConfig_InvalidDisplayMode(t *testing.T) {
	err := validateConfig("yes", "Right", "no", "Left", 30, "invalid", "dark")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "display mode must be")
}

func TestValidateConfig_InvalidTheme(t *testing.T) {
	err := validateConfig("yes", "Right", "no", "Left", 30, "combined", "neon")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "theme must be")
}

func TestValidateConfig_Valid(t *testing.T) {
	tests := []struct {
		name           string
		triggerFor     string
		triggerAgainst string
		labelAgainst   string
		labelFor       string
		memorySeconds  int
		displayMode    string
		theme          string
	}{
		{
			name:           "default values",
			triggerFor:     "yes",
			triggerAgainst: "no",
			labelAgainst:   "Against",
			labelFor:       "For",
			memorySeconds:  30,
			displayMode:    "combined",
			theme:          "dark",
		},
		{
			name:           "custom emotes with light theme",
			triggerFor:     "PogChamp",
			triggerAgainst: "NotLikeThis",
			labelAgainst:   "Happy",
			labelFor:       "Sad",
			memorySeconds:  60,
			displayMode:    "split",
			theme:          "light",
		},
		{
			name:           "min values",
			triggerFor:     "yes",
			triggerAgainst: "no",
			labelAgainst:   "Left",
			labelFor:       "Right",
			memorySeconds:  5,
			displayMode:    "combined",
			theme:          "dark",
		},
		{
			name:           "max values",
			triggerFor:     "yes",
			triggerAgainst: "no",
			labelAgainst:   "Left",
			labelFor:       "Right",
			memorySeconds:  120,
			displayMode:    "split",
			theme:          "light",
		},
		{
			name:           "infinite memory",
			triggerFor:     "yes",
			triggerAgainst: "no",
			labelAgainst:   "Left",
			labelFor:       "Right",
			memorySeconds:  43200,
			displayMode:    "combined",
			theme:          "dark",
		},
		{
			name:           "with whitespace (trimmed)",
			triggerFor:     "  yes  ",
			triggerAgainst: "  no  ",
			labelAgainst:   "  Left  ",
			labelFor:       "  Right  ",
			memorySeconds:  30,
			displayMode:    "combined",
			theme:          "dark",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateConfig(tt.triggerFor, tt.labelFor, tt.triggerAgainst, tt.labelAgainst, tt.memorySeconds, tt.displayMode, tt.theme)
			assert.NoError(t, err)
		})
	}
}
