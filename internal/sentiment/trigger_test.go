package sentiment

import (
	"testing"

	"github.com/pscheid92/chatpulse/internal/models"
	"github.com/stretchr/testify/assert"
)

func TestMatchTrigger_ForTrigger(t *testing.T) {
	config := &models.ConfigSnapshot{ForTrigger: "yes", AgainstTrigger: "no"}
	assert.Equal(t, 10.0, MatchTrigger("I vote yes!", config))
}

func TestMatchTrigger_AgainstTrigger(t *testing.T) {
	config := &models.ConfigSnapshot{ForTrigger: "yes", AgainstTrigger: "no"}
	assert.Equal(t, -10.0, MatchTrigger("vote no", config))
}

func TestMatchTrigger_NoMatch(t *testing.T) {
	config := &models.ConfigSnapshot{ForTrigger: "yes", AgainstTrigger: "no"}
	assert.Equal(t, 0.0, MatchTrigger("hello world", config))
}

func TestMatchTrigger_CaseInsensitive(t *testing.T) {
	config := &models.ConfigSnapshot{ForTrigger: "yes", AgainstTrigger: "no"}
	assert.Equal(t, 10.0, MatchTrigger("YES PLEASE!", config))
	assert.Equal(t, -10.0, MatchTrigger("NO WAY", config))
}

func TestMatchTrigger_ForTriggerPriority(t *testing.T) {
	config := &models.ConfigSnapshot{ForTrigger: "yes", AgainstTrigger: "yesno"}
	// "yesno" contains "yes" (ForTrigger), so ForTrigger matches first
	assert.Equal(t, 10.0, MatchTrigger("yesno", config))
}

func TestMatchTrigger_NilConfig(t *testing.T) {
	assert.Equal(t, 0.0, MatchTrigger("yes", nil))
}

func TestMatchTrigger_EmptyMessage(t *testing.T) {
	config := &models.ConfigSnapshot{ForTrigger: "yes", AgainstTrigger: "no"}
	assert.Equal(t, 0.0, MatchTrigger("", config))
}
