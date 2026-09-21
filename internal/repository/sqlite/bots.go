package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"tgtriage/internal/domain"
)

// BotRepo implements domain.BotRepository.
type BotRepo struct{ db *sql.DB }

var _ domain.BotRepository = (*BotRepo)(nil)

const botColumns = `id, token, username, label, active, sensitivity, ai_chain,
	hd_enabled, hd_group_id, hd_triage_enabled, hd_about, hd_greeting_enabled, hd_greeting_text,
	hd_autoreply_enabled, hd_autoreply_text, hd_hours_enabled, hd_hours_start, hd_hours_end, hd_hours_days,
	hd_offhours_text, hd_reminder_minutes, hd_spam_screen, hd_spam_captcha, created_at, updated_at`

func scanBot(sc interface{ Scan(...any) error }) (domain.Bot, error) {
	var (
		b                                                                                      domain.Bot
		active, hdEnabled, hdTriage, hdGreeting, hdAutoreply, hdHours, spamScreen, spamCaptcha int
		sensitivity, aiChain                                                                   string
		cr, upd                                                                                int64
	)
	err := sc.Scan(&b.ID, &b.Token, &b.Username, &b.Label, &active, &sensitivity, &aiChain,
		&hdEnabled, &b.Helpdesk.GroupID, &hdTriage, &b.Helpdesk.About, &hdGreeting, &b.Helpdesk.GreetingText,
		&hdAutoreply, &b.Helpdesk.AutoReplyText, &hdHours, &b.Helpdesk.HoursStart, &b.Helpdesk.HoursEnd, &b.Helpdesk.HoursDays,
		&b.Helpdesk.OffHoursText, &b.Helpdesk.ReminderMinutes, &spamScreen, &spamCaptcha, &cr, &upd)
	if err != nil {
		return b, err
	}
	b.Active = active == 1
	b.Sensitivity = domain.Sensitivity(sensitivity)
	if aiChain != "" {
		_ = json.Unmarshal([]byte(aiChain), &b.AIChain)
	}
	b.Helpdesk.Enabled = hdEnabled == 1
	b.Helpdesk.TriageEnabled = hdTriage == 1
	b.Helpdesk.GreetingEnabled = hdGreeting == 1
	b.Helpdesk.AutoReplyEnabled = hdAutoreply == 1
	b.Helpdesk.HoursEnabled = hdHours == 1
	b.Helpdesk.SpamScreen = spamScreen == 1
	b.Helpdesk.SpamCaptcha = spamCaptcha == 1
	b.CreatedAt = fromUnix(cr)
	b.UpdatedAt = fromUnix(upd)
	return b, nil
}

func (r *BotRepo) List(ctx context.Context) ([]domain.Bot, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+botColumns+` FROM bots ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list bots: %w", err)
	}
	defer rows.Close()
	var out []domain.Bot
	for rows.Next() {
		b, err := scanBot(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (r *BotRepo) Get(ctx context.Context, id int64) (*domain.Bot, error) {
	b, err := scanBot(r.db.QueryRowContext(ctx, `SELECT `+botColumns+` FROM bots WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get bot: %w", err)
	}
	return &b, nil
}

func (r *BotRepo) Save(ctx context.Context, b *domain.Bot) error {
	now := time.Now()
	if b.CreatedAt.IsZero() {
		b.CreatedAt = now
	}
	b.UpdatedAt = now
	chain, _ := json.Marshal(b.AIChain)
	if b.ID == 0 {
		res, err := r.db.ExecContext(ctx, `INSERT INTO bots (`+botColumns+`) VALUES (NULL,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			b.Token, b.Username, b.Label, boolInt(b.Active), string(b.Sensitivity), string(chain),
			boolInt(b.Helpdesk.Enabled), b.Helpdesk.GroupID, boolInt(b.Helpdesk.TriageEnabled), b.Helpdesk.About,
			boolInt(b.Helpdesk.GreetingEnabled), b.Helpdesk.GreetingText, boolInt(b.Helpdesk.AutoReplyEnabled),
			b.Helpdesk.AutoReplyText, boolInt(b.Helpdesk.HoursEnabled), b.Helpdesk.HoursStart, b.Helpdesk.HoursEnd,
			b.Helpdesk.HoursDays, b.Helpdesk.OffHoursText, b.Helpdesk.ReminderMinutes, boolInt(b.Helpdesk.SpamScreen), boolInt(b.Helpdesk.SpamCaptcha),
			toUnix(b.CreatedAt), toUnix(b.UpdatedAt))
		if err != nil {
			if isUniqueViolation(err) {
				return fmt.Errorf("%w: этот токен уже добавлен", domain.ErrInvalidInput)
			}
			return fmt.Errorf("insert bot: %w", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		b.ID = id
		return nil
	}
	_, err := r.db.ExecContext(ctx, `UPDATE bots SET token=?, username=?, label=?, active=?, sensitivity=?, ai_chain=?,
		hd_enabled=?, hd_group_id=?, hd_triage_enabled=?, hd_about=?, hd_greeting_enabled=?, hd_greeting_text=?,
		hd_autoreply_enabled=?, hd_autoreply_text=?, hd_hours_enabled=?, hd_hours_start=?, hd_hours_end=?, hd_hours_days=?,
		hd_offhours_text=?, hd_reminder_minutes=?, hd_spam_screen=?, hd_spam_captcha=?, updated_at=? WHERE id=?`,
		b.Token, b.Username, b.Label, boolInt(b.Active), string(b.Sensitivity), string(chain),
		boolInt(b.Helpdesk.Enabled), b.Helpdesk.GroupID, boolInt(b.Helpdesk.TriageEnabled), b.Helpdesk.About,
		boolInt(b.Helpdesk.GreetingEnabled), b.Helpdesk.GreetingText, boolInt(b.Helpdesk.AutoReplyEnabled),
		b.Helpdesk.AutoReplyText, boolInt(b.Helpdesk.HoursEnabled), b.Helpdesk.HoursStart, b.Helpdesk.HoursEnd,
		b.Helpdesk.HoursDays, b.Helpdesk.OffHoursText, b.Helpdesk.ReminderMinutes, boolInt(b.Helpdesk.SpamScreen), boolInt(b.Helpdesk.SpamCaptcha),
		toUnix(b.UpdatedAt), b.ID)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("%w: этот токен уже добавлен", domain.ErrInvalidInput)
		}
		return fmt.Errorf("update bot: %w", err)
	}
	return nil
}

func (r *BotRepo) Delete(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM bots WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete bot: %w", err)
	}
	return nil
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
