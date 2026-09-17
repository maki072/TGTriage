package domain

import (
	"context"
	"strconv"
	"strings"
	"time"
)

// Bot is an additional Telegram bot the owner hands out to a client organization for its own
// support desk, on top of the main bot (whose token comes from the environment and whose data
// keeps using the legacy HelpdeskConnectionID / Settings.Helpdesk untouched). Sensitivity and
// AIChain are optional overrides of the shared Settings: a zero value means "inherit".
type Bot struct {
	ID        int64
	Token     string
	Username  string // cached from getMe, for display only
	Label     string // organization name shown in the panel
	Active    bool
	CreatedAt time.Time
	UpdatedAt time.Time

	Sensitivity Sensitivity // "" = inherit the shared setting
	AIChain     []AIKey     // empty = inherit the shared chain

	// Helpdesk is this bot's own, independent support desk configuration — its group, greeting,
	// hours and so on are the identity of the client organization, not a technical AI setting, so
	// unlike Sensitivity/AIChain they are not merged with the shared defaults.
	Helpdesk HelpdeskSettings
}

// EffectiveSensitivity returns the bot's own sensitivity override, or global when unset.
func (b Bot) EffectiveSensitivity(global Sensitivity) Sensitivity {
	if b.Sensitivity == "" {
		return global
	}
	return b.Sensitivity
}

// EffectiveAIChain returns the bot's own AI chain override, or global when unset.
func (b Bot) EffectiveAIChain(global []AIKey) []AIKey {
	if len(b.AIChain) == 0 {
		return global
	}
	return b.AIChain
}

// BotRepository stores additional bots.
type BotRepository interface {
	List(ctx context.Context) ([]Bot, error)
	Get(ctx context.Context, id int64) (*Bot, error)
	Save(ctx context.Context, b *Bot) error
	Delete(ctx context.Context, id int64) error
}

// helpdeskConnPrefix marks a helpdesk connection_id (in tasks/messages/analyses) as belonging to
// an additional bot rather than the main one.
const helpdeskConnPrefix = "helpdesk:"

// HelpdeskConnectionFor returns the connection_id used for a bot's helpdesk tickets/messages:
// the legacy literal for the main bot (0), "helpdesk:<id>" for an additional one.
func HelpdeskConnectionFor(botID int64) string {
	if botID == 0 {
		return HelpdeskConnectionID
	}
	return helpdeskConnPrefix + strconv.FormatInt(botID, 10)
}

// IsHelpdeskConnection reports whether connID is a helpdesk connection of any bot.
func IsHelpdeskConnection(connID string) bool {
	return connID == HelpdeskConnectionID || strings.HasPrefix(connID, helpdeskConnPrefix)
}

// ParseHelpdeskBotID extracts the bot id from a helpdesk connection_id (0 for the main bot or a
// non-helpdesk connection).
func ParseHelpdeskBotID(connID string) int64 {
	rest, ok := strings.CutPrefix(connID, helpdeskConnPrefix)
	if !ok {
		return 0
	}
	id, err := strconv.ParseInt(rest, 10, 64)
	if err != nil {
		return 0
	}
	return id
}
