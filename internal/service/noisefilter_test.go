package service

import (
	"testing"

	"tgtriage/internal/domain"
)

func TestIsHeuristicNoise(t *testing.T) {
	msg := func(text string) domain.Message { return domain.Message{Text: text} }

	noise := [][]domain.Message{
		{msg("спасибо")},
		{msg("Спасибо!")},
		{msg("  спс  ")},
		{msg("ок, спасибо")},
		{msg("👍")},
		{msg("🙏😊")},
		{msg("thanks")},
		{msg("Thank you!")},
		{msg("понял"), msg("спасибо")}, // whole batch is noise, message by message
	}
	for _, batch := range noise {
		if !isHeuristicNoise(batch) {
			t.Errorf("expected noise: %+v", batch)
		}
	}

	notNoise := [][]domain.Message{
		{msg("да")}, // bare yes/no is context-dependent, never auto-skip
		{msg("нет")},
		{msg("спасибо, но у меня не работает вход")},     // real request riding on a thank-you
		{msg("понял"), msg("а можешь ещё глянуть баг?")}, // one noise + one real message in the batch
		{msg("")},     // empty (e.g. photo-only) must reach the model
		{msg("окда")}, // similar-looking but not an exact match
		{msg("ок 👍 и ещё пришли отчёт")},
	}
	for _, batch := range notNoise {
		if isHeuristicNoise(batch) {
			t.Errorf("expected NOT noise: %+v", batch)
		}
	}
}
