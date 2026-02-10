package sentiment

import (
	"testing"

	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/stretchr/testify/assert"
)

func TestMatchTrigger_ForTrigger(t *testing.T) {
	config := &domain.ConfigSnapshot{ForTrigger: "yes", AgainstTrigger: "no"}
	assert.Equal(t, 10.0, MatchTrigger("I vote yes!", config))
}

func TestMatchTrigger_AgainstTrigger(t *testing.T) {
	config := &domain.ConfigSnapshot{ForTrigger: "yes", AgainstTrigger: "no"}
	assert.Equal(t, -10.0, MatchTrigger("vote no", config))
}

func TestMatchTrigger_NoMatch(t *testing.T) {
	config := &domain.ConfigSnapshot{ForTrigger: "yes", AgainstTrigger: "no"}
	assert.Equal(t, 0.0, MatchTrigger("hello world", config))
}

func TestMatchTrigger_CaseInsensitive(t *testing.T) {
	config := &domain.ConfigSnapshot{ForTrigger: "yes", AgainstTrigger: "no"}
	assert.Equal(t, 10.0, MatchTrigger("YES PLEASE!", config))
	assert.Equal(t, -10.0, MatchTrigger("NO WAY", config))
}

func TestMatchTrigger_ForTriggerPriority(t *testing.T) {
	config := &domain.ConfigSnapshot{ForTrigger: "yes", AgainstTrigger: "yesno"}
	// "yesno" contains "yes" (ForTrigger), so ForTrigger matches first
	assert.Equal(t, 10.0, MatchTrigger("yesno", config))
}

func TestMatchTrigger_NilConfig(t *testing.T) {
	assert.Equal(t, 0.0, MatchTrigger("yes", nil))
}

func TestMatchTrigger_EmptyMessage(t *testing.T) {
	config := &domain.ConfigSnapshot{ForTrigger: "yes", AgainstTrigger: "no"}
	assert.Equal(t, 0.0, MatchTrigger("", config))
}

func TestMatchTrigger_EmptyTriggers(t *testing.T) {
	config := &domain.ConfigSnapshot{ForTrigger: "", AgainstTrigger: ""}
	assert.Equal(t, 0.0, MatchTrigger("hello world", config))
	assert.Equal(t, 0.0, MatchTrigger("yes", config))
	assert.Equal(t, 0.0, MatchTrigger("no", config))
}

func TestMatchTrigger_OneEmptyTrigger(t *testing.T) {
	// Empty for-trigger should not match, but against-trigger should
	config := &domain.ConfigSnapshot{ForTrigger: "", AgainstTrigger: "no"}
	assert.Equal(t, 0.0, MatchTrigger("hello", config))
	assert.Equal(t, -10.0, MatchTrigger("no way", config))

	// Empty against-trigger should not match, but for-trigger should
	config2 := &domain.ConfigSnapshot{ForTrigger: "yes", AgainstTrigger: ""}
	assert.Equal(t, 10.0, MatchTrigger("yes please", config2))
	assert.Equal(t, 0.0, MatchTrigger("hello", config2))
}
