package server

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Unit tests for validateConfig (no external dependencies)

func TestValidateConfig_EmptyTriggerFor(t *testing.T) {
	err := validateConfig("  ", "no", "Left", "Right", 1.0)
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
	err := validateConfig(string(spaces), "no", "Left", "Right", 1.0)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "for trigger cannot be empty")
}

func TestValidateConfig_EmptyTriggerAgainst(t *testing.T) {
	err := validateConfig("yes", "  ", "Left", "Right", 1.0)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "against trigger cannot be empty")
}

func TestValidateConfig_ForTriggerTooLong(t *testing.T) {
	longTrigger := string(make([]byte, 501)) // 501 characters
	err := validateConfig(longTrigger, "no", "Left", "Right", 1.0)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "for trigger exceeds 500 characters")
}

func TestValidateConfig_AgainstTriggerTooLong(t *testing.T) {
	longTrigger := string(make([]byte, 501)) // 501 characters
	err := validateConfig("yes", longTrigger, "Left", "Right", 1.0)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "against trigger exceeds 500 characters")
}

func TestValidateConfig_LeftLabelTooLong(t *testing.T) {
	longLabel := string(make([]byte, 51)) // 51 characters
	err := validateConfig("yes", "no", longLabel, "Right", 1.0)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "left label exceeds 50 characters")
}

func TestValidateConfig_RightLabelTooLong(t *testing.T) {
	longLabel := string(make([]byte, 51)) // 51 characters
	err := validateConfig("yes", "no", "Left", longLabel, 1.0)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "right label exceeds 50 characters")
}

func TestValidateConfig_DecayTooLow(t *testing.T) {
	err := validateConfig("yes", "no", "Left", "Right", 0.05)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "decay speed must be between 0.1 and 2.0")
}

func TestValidateConfig_DecayTooHigh(t *testing.T) {
	err := validateConfig("yes", "no", "Left", "Right", 2.5)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "decay speed must be between 0.1 and 2.0")
}

func TestValidateConfig_IdenticalTriggers(t *testing.T) {
	err := validateConfig("PogChamp", "pogchamp", "Left", "Right", 1.0)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "for trigger and against trigger must be different")
}

func TestValidateConfig_Valid(t *testing.T) {
	tests := []struct {
		name           string
		triggerFor     string
		triggerAgainst string
		labelLeft      string
		labelRight     string
		decaySpeed     float64
	}{
		{
			name:           "default values",
			triggerFor:     "yes",
			triggerAgainst: "no",
			labelLeft:      "Against",
			labelRight:     "For",
			decaySpeed:     0.5,
		},
		{
			name:           "custom emotes",
			triggerFor:     "PogChamp",
			triggerAgainst: "NotLikeThis",
			labelLeft:      "Happy",
			labelRight:     "Sad",
			decaySpeed:     1.0,
		},
		{
			name:           "min decay",
			triggerFor:     "yes",
			triggerAgainst: "no",
			labelLeft:      "Left",
			labelRight:     "Right",
			decaySpeed:     0.1,
		},
		{
			name:           "max decay",
			triggerFor:     "yes",
			triggerAgainst: "no",
			labelLeft:      "Left",
			labelRight:     "Right",
			decaySpeed:     2.0,
		},
		{
			name:           "with whitespace (trimmed)",
			triggerFor:     "  yes  ",
			triggerAgainst: "  no  ",
			labelLeft:      "  Left  ",
			labelRight:     "  Right  ",
			decaySpeed:     1.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateConfig(tt.triggerFor, tt.triggerAgainst, tt.labelLeft, tt.labelRight, tt.decaySpeed)
			assert.NoError(t, err)
		})
	}
}
