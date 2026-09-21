package service

import (
	"strings"
	"unicode"
)

// IsDoneMessage reports whether the owner's own chat message declares the work finished:
// "готово", "сделал", "готово, отключился", "всё исправил". It is deliberately conservative — a wrong
// close is worse than a missed one — so it only accepts short statements and rejects questions,
// negations and promises ("не готово", "будет готово завтра", "когда сделаешь?").
func IsDoneMessage(text string) bool {
	if strings.Contains(text, "?") {
		return false
	}
	words := doneTokens(text)
	if len(words) == 0 || len(words) > maxDoneWords {
		return false
	}
	found := false
	for _, w := range words {
		if _, no := notDoneWords[w]; no {
			return false
		}
		if _, ok := doneWords[w]; ok {
			found = true
		}
	}
	return found
}

const maxDoneWords = 7

// doneTokens lowercases, folds ё to е and splits into letter/digit runs.
func doneTokens(text string) []string {
	text = strings.ReplaceAll(strings.ToLower(text), "ё", "е")
	return strings.FieldsFunc(text, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

func wordSet(words ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(words))
	for _, w := range words {
		m[w] = struct{}{}
	}
	return m
}

// doneWords are the explicit completion forms; matching whole words avoids stem accidents.
var doneWords = wordSet(
	"готово", "сделано", "сделал", "сделала", "сделали", "выполнено", "выполнил", "выполнила", "выполнили",
	"решено", "решил", "решила", "решили", "исправлено", "исправил", "исправила", "исправили",
	"поправлено", "поправил", "поправила", "поправили", "починено", "починил", "починила", "починили",
	"отключено", "отключил", "отключила", "отключили", "отключился", "отключилась", "отключились",
	"включено", "включил", "включила", "включили", "включился", "включилась", "включились",
	"настроено", "настроил", "настроила", "настроили", "установлено", "установил", "установила", "установили",
	"обновлено", "обновил", "обновила", "обновили", "отправлено", "отправил", "отправила", "отправили",
	"закрыто", "закрыл", "закрыла", "закрыли", "завершено", "завершил", "завершила", "завершили",
	"оплачено", "оплатил", "оплатила", "оплатили", "выложил", "выложила", "залил", "залила", "done",
)

// notDoneWords turn a sentence into a negation, question, promise or condition.
var notDoneWords = wordSet(
	"не", "нет", "ни", "нельзя", "пока", "еще", "почти", "будет", "буду", "будем", "будут", "сделаю", "сделаем",
	"займусь", "займемся", "скоро", "потом", "позже", "завтра", "когда", "если", "надо", "нужно", "можно",
	"давай", "давайте", "возможно", "попробую", "посмотрю", "проверю", "постараюсь", "успею", "должно",
	"должен", "должна", "хочу", "хотел", "хотела", "планирую", "собираюсь", "как", "ли", "почему",
)
