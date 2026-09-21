package service

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"tgtriage/internal/domain"
)

// FieldKind tells the Mini App how to render a setting.
type FieldKind string

const (
	KindBool   FieldKind = "bool"
	KindInt    FieldKind = "int"
	KindString FieldKind = "string"
	KindText   FieldKind = "text"   // multi-line string
	KindSelect FieldKind = "select" // one of Options
	KindTime   FieldKind = "time"   // HH:MM
	KindList   FieldKind = "list"   // []string, one item per line
	KindDays   FieldKind = "days"   // ISO weekday digits
	KindModel  FieldKind = "model"  // model id with the provider's presets as suggestions
)

// FieldOption is a choice of a select field.
type FieldOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// SettingGroup is a titled section of the settings screen.
type SettingGroup struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// SettingGroups lists sections in display order.
var SettingGroups = []SettingGroup{
	{"helpdesk", "🎧 Хелпдеск"},
	{"bots", "🏢 Боты организаций"},
	{"ai", "🤖 AI-провайдеры"},
	{"triage", "🎯 Триаж"},
	{"general", "⚙️ Общие"},
	{"backup", "💾 Резервные копии"},
}

// SettingField describes one persisted setting: its DB key, the legacy env variable it is seeded from,
// how to render it and how to convert it between the Settings struct and its storage string.
type SettingField struct {
	Key      string        `json:"key"`
	Env      string        `json:"-"`
	Group    string        `json:"group"`
	Label    string        `json:"label"`
	Desc     string        `json:"desc,omitempty"`
	Kind     FieldKind     `json:"kind"`
	Options  []FieldOption `json:"options,omitempty"`
	Min      int64         `json:"min,omitempty"`
	Max      int64         `json:"max,omitempty"`
	Restart  bool          `json:"restart,omitempty"`  // applied after a service restart
	Provider string        `json:"provider,omitempty"` // KindModel: provider whose presets to suggest
	Default  string        `json:"-"`

	get func(*domain.Settings) string
	set func(*domain.Settings, string) error // parses and validates the storage string
}

// Value returns the field value typed for JSON.
func (f SettingField) Value(s *domain.Settings) any {
	raw := f.get(s)
	switch f.Kind {
	case KindBool:
		b, _ := strconv.ParseBool(raw)
		return b
	case KindInt:
		n, _ := strconv.ParseInt(raw, 10, 64)
		return n
	case KindList:
		var l []string
		_ = json.Unmarshal([]byte(raw), &l)
		if l == nil {
			l = []string{}
		}
		return l
	}
	return raw
}

// SetJSON applies a JSON value (as sent by the Mini App) to s.
func (f SettingField) SetJSON(s *domain.Settings, raw json.RawMessage) error {
	var str string
	switch f.Kind {
	case KindBool:
		var b bool
		if err := json.Unmarshal(raw, &b); err != nil {
			return f.invalid("ожидается да/нет")
		}
		str = strconv.FormatBool(b)
	case KindInt:
		var n json.Number
		if err := json.Unmarshal(raw, &n); err != nil {
			var sv string // a numeric string typed into an input is fine too
			if json.Unmarshal(raw, &sv) != nil {
				return f.invalid("ожидается число")
			}
			n = json.Number(strings.TrimSpace(sv))
		}
		str = n.String()
	case KindList:
		var l []string
		if err := json.Unmarshal(raw, &l); err != nil {
			return f.invalid("ожидается список строк")
		}
		b, _ := json.Marshal(l)
		str = string(b)
	default:
		if err := json.Unmarshal(raw, &str); err != nil {
			return f.invalid("ожидается строка")
		}
	}
	return f.set(s, str)
}

func (f SettingField) invalid(msg string) error {
	return fmt.Errorf("%w: %s: %s", domain.ErrInvalidInput, f.Label, msg)
}

// ---------- field constructors ----------

func boolField(key, env, group, label, desc, def string, ptr func(*domain.Settings) *bool) SettingField {
	f := SettingField{Key: key, Env: env, Group: group, Label: label, Desc: desc, Kind: KindBool, Default: def}
	f.get = func(s *domain.Settings) string { return strconv.FormatBool(*ptr(s)) }
	f.set = func(s *domain.Settings, v string) error {
		b, err := strconv.ParseBool(strings.TrimSpace(v))
		if err != nil {
			return f.invalid("ожидается true/false")
		}
		*ptr(s) = b
		return nil
	}
	return f
}

func intField(key, env, group, label, desc, def string, minV, maxV int, ptr func(*domain.Settings) *int) SettingField {
	f := SettingField{Key: key, Env: env, Group: group, Label: label, Desc: desc, Kind: KindInt,
		Min: int64(minV), Max: int64(maxV), Default: def}
	f.get = func(s *domain.Settings) string { return strconv.Itoa(*ptr(s)) }
	f.set = func(s *domain.Settings, v string) error {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || n < minV || n > maxV {
			return f.invalid(fmt.Sprintf("целое число от %d до %d", minV, maxV))
		}
		*ptr(s) = n
		return nil
	}
	return f
}

func stringField(key, env, group, label, desc, def string, kind FieldKind, check func(string) (string, error),
	ptr func(*domain.Settings) *string) SettingField {
	f := SettingField{Key: key, Env: env, Group: group, Label: label, Desc: desc, Kind: kind, Default: def}
	f.get = func(s *domain.Settings) string { return *ptr(s) }
	f.set = func(s *domain.Settings, v string) error {
		if kind != KindText {
			v = strings.TrimSpace(v)
		}
		if check != nil {
			norm, err := check(v)
			if err != nil {
				return f.invalid(err.Error())
			}
			v = norm
		}
		*ptr(s) = v
		return nil
	}
	return f
}

func listField(key, env, group, label, desc, def string, check func(string) error, ptr func(*domain.Settings) *[]string) SettingField {
	f := SettingField{Key: key, Env: env, Group: group, Label: label, Desc: desc, Kind: KindList, Default: def}
	f.get = func(s *domain.Settings) string {
		b, _ := json.Marshal(*ptr(s))
		return string(b)
	}
	f.set = func(s *domain.Settings, v string) error {
		var items []string
		v = strings.TrimSpace(v)
		if strings.HasPrefix(v, "[") {
			if err := json.Unmarshal([]byte(v), &items); err != nil {
				return f.invalid("некорректный список")
			}
		} else { // env style: comma separated
			items = strings.Split(v, ",")
		}
		out := make([]string, 0, len(items))
		for _, it := range items {
			if it = strings.TrimSpace(it); it == "" {
				continue
			}
			if check != nil {
				if err := check(it); err != nil {
					return f.invalid(err.Error())
				}
			}
			out = append(out, it)
		}
		if len(out) > 30 {
			return f.invalid("не больше 30 элементов")
		}
		*ptr(s) = out
		return nil
	}
	return f
}

// ---------- validators ----------

func checkModel(v string) (string, error) {
	if !ValidModelName(v) {
		return "", fmt.Errorf("некорректный идентификатор модели")
	}
	return v, nil
}

func checkModelItem(v string) error { _, err := checkModel(v); return err }

func checkURL(v string) (string, error) {
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", fmt.Errorf("нужен адрес вида https://host")
	}
	return strings.TrimRight(v, "/"), nil
}

func checkPublicURL(v string) (string, error) {
	if v == "" {
		return "", nil
	}
	if !strings.HasPrefix(v, "https://") {
		return "", fmt.Errorf("адрес должен начинаться с https:// (Telegram не открывает Mini App по http)")
	}
	return checkURL(v)
}

func checkClock(v string) (string, error) {
	h, m, err := ParseClock(v)
	if err != nil {
		return "", fmt.Errorf("время в формате ЧЧ:ММ")
	}
	return fmt.Sprintf("%02d:%02d", h, m), nil
}

func checkTimezone(v string) (string, error) {
	if v == "" {
		return "", fmt.Errorf("укажите часовой пояс, например Europe/Moscow")
	}
	if _, err := time.LoadLocation(v); err != nil {
		return "", fmt.Errorf("неизвестный часовой пояс")
	}
	return v, nil
}

func checkDays(v string) (string, error) {
	var b strings.Builder
	for d := '1'; d <= '7'; d++ {
		if strings.ContainsRune(v, d) {
			b.WriteRune(d)
		}
	}
	for _, r := range v {
		if r < '1' || r > '7' {
			return "", fmt.Errorf("дни недели цифрами 1–7")
		}
	}
	return b.String(), nil
}

func maxLen(n int) func(string) (string, error) {
	return func(v string) (string, error) {
		if len([]rune(v)) > n {
			return "", fmt.Errorf("не длиннее %d символов", n)
		}
		return v, nil
	}
}

func selectOf(options ...FieldOption) func(string) (string, error) {
	return func(v string) (string, error) {
		for _, o := range options {
			if o.Value == v {
				return v, nil
			}
		}
		return "", fmt.Errorf("недопустимое значение")
	}
}

// ---------- registry ----------

var (
	sensitivityOptions = []FieldOption{{"low", "Низкая"}, {"medium", "Средняя"}, {"high", "Высокая"}}
	effortOptions      = []FieldOption{{"", "по умолчанию API"}, {"low", "low"}, {"medium", "medium"}, {"high", "high"}, {"xhigh", "xhigh"}, {"max", "max"}}
)

func providerFields(p, title, envPrefix, model, presets, maxTokens, maxTokensMax, baseURL string) []SettingField {
	ps := func(s *domain.Settings) *domain.ProviderSettings { return s.Provider(p) }
	mf := stringField("ai."+p+"_model", envPrefix+"_MODEL", "ai", "Модель "+title, "", model, KindModel, checkModel,
		func(s *domain.Settings) *string { return &ps(s).Model })
	mf.Provider = p
	maxT, _ := strconv.Atoi(maxTokensMax)
	return []SettingField{
		mf,
		listField("ai."+p+"_presets", envPrefix+"_MODEL_PRESETS", "ai", "Пресеты моделей "+title,
			"Подсказки при выборе модели", presets, checkModelItem, func(s *domain.Settings) *[]string { return &ps(s).Presets }),
		intField("ai."+p+"_max_tokens", envPrefix+"_MAX_TOKENS", "ai", "Max tokens "+title, "", maxTokens, 1024, maxT,
			func(s *domain.Settings) *int { return &ps(s).MaxTokens }),
		stringField("ai."+p+"_base_url", baseURLEnv(p), "ai", "API URL "+title, "", baseURL, KindString, checkURL,
			func(s *domain.Settings) *string { return &ps(s).BaseURL }),
	}
}

func baseURLEnv(p string) string {
	if p == domain.ProviderClaude {
		return "ANTHROPIC_BASE_URL"
	}
	return strings.ToUpper(p) + "_BASE_URL"
}

func buildFields() []SettingField {
	var fields []SettingField
	add := func(f ...SettingField) { fields = append(fields, f...) }
	hd := func(s *domain.Settings) *domain.HelpdeskSettings { return &s.Helpdesk }

	// helpdesk
	add(
		boolField("helpdesk.enabled", "HELPDESK_ENABLED", "helpdesk", "Хелпдеск включён",
			"Пользователи пишут боту, операторы отвечают в темах супергруппы", "false",
			func(s *domain.Settings) *bool { return &hd(s).Enabled }),
		func() SettingField {
			f := SettingField{Key: "helpdesk.group_id", Env: "HELPDESK_GROUP_ID", Group: "helpdesk", Label: "ID супергруппы",
				Desc: "Супергруппа с включёнными темами, бот — админ с правом «Управление темами». ID вида -100…",
				Kind: KindInt, Default: "0"}
			f.get = func(s *domain.Settings) string { return strconv.FormatInt(hd(s).GroupID, 10) }
			f.set = func(s *domain.Settings, v string) error {
				n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
				if err != nil || n > 0 {
					return f.invalid("ID супергруппы — отрицательное число вида -1001234567890")
				}
				hd(s).GroupID = n
				return nil
			}
			return f
		}(),
		boolField("helpdesk.triage_enabled", "HELPDESK_TRIAGE_ENABLED", "helpdesk", "Автоматические тикеты",
			"LLM анализирует сообщения пользователей и заводит тикеты", "true",
			func(s *domain.Settings) *bool { return &hd(s).TriageEnabled }),
		stringField("helpdesk.about", "HELPDESK_ABOUT", "helpdesk", "О сервисе поддержки",
			"Чем занимается поддержка — помогает модели отличать обращения от шума. Пусто — берётся «О владельце»", "", KindText, maxLen(2000),
			func(s *domain.Settings) *string { return &hd(s).About }),
		boolField("helpdesk.greeting_enabled", "", "helpdesk", "Приветствие на /start", "", "true",
			func(s *domain.Settings) *bool { return &hd(s).GreetingEnabled }),
		stringField("helpdesk.greeting_text", "", "helpdesk", "Текст приветствия", "",
			"Здравствуйте! Напишите ваш вопрос одним или несколькими сообщениями — мы ответим прямо здесь.",
			KindText, maxLen(3000), func(s *domain.Settings) *string { return &hd(s).GreetingText }),
		boolField("helpdesk.autoreply_enabled", "", "helpdesk", "Автоответ на обращение",
			"Отправляется на первое сообщение, пока оператор не ответил", "false",
			func(s *domain.Settings) *bool { return &hd(s).AutoReplyEnabled }),
		stringField("helpdesk.autoreply_text", "", "helpdesk", "Текст автоответа", "",
			"Спасибо, сообщение получено. Оператор ответит в ближайшее время.",
			KindText, maxLen(3000), func(s *domain.Settings) *string { return &hd(s).AutoReplyText }),
		boolField("helpdesk.hours_enabled", "", "helpdesk", "Рабочие часы",
			"Вне рабочих часов пользователю уходит отдельный автоответ, напоминания операторам не приходят", "false",
			func(s *domain.Settings) *bool { return &hd(s).HoursEnabled }),
		stringField("helpdesk.hours_start", "", "helpdesk", "Начало рабочего дня", "", "09:00", KindTime, checkClock,
			func(s *domain.Settings) *string { return &hd(s).HoursStart }),
		stringField("helpdesk.hours_end", "", "helpdesk", "Конец рабочего дня", "", "18:00", KindTime, checkClock,
			func(s *domain.Settings) *string { return &hd(s).HoursEnd }),
		stringField("helpdesk.hours_days", "", "helpdesk", "Рабочие дни", "", "12345", KindDays, checkDays,
			func(s *domain.Settings) *string { return &hd(s).HoursDays }),
		stringField("helpdesk.offhours_text", "", "helpdesk", "Автоответ вне рабочих часов", "{hours} — рабочие часы",
			"Сейчас нерабочее время. Сообщение получено, ответим в рабочие часы: {hours}.",
			KindText, maxLen(3000), func(s *domain.Settings) *string { return &hd(s).OffHoursText }),
		intField("helpdesk.reminder_minutes", "", "helpdesk", "Напоминание о неотвеченных, мин",
			"Бот пишет в тему, если пользователь ждёт ответа дольше. 0 — выключено", "15", 0, 1440,
			func(s *domain.Settings) *int { return &hd(s).ReminderMinutes }),
	)

	// AI
	add(providerFields(domain.ProviderClaude, "Claude", "CLAUDE", "claude-opus-5", "claude-opus-5,claude-sonnet-5,claude-haiku-4-5", "16000", "64000", "https://api.anthropic.com")...)
	add(providerFields(domain.ProviderGemini, "Gemini", "GEMINI", "gemini-3.6-flash", "gemini-3.6-flash,gemini-3.8-flash,gemini-3.1-pro-preview", "8192", "65536", "https://generativelanguage.googleapis.com")...)
	add(providerFields(domain.ProviderGroq, "Groq", "GROQ", "openai/gpt-oss-120b", "openai/gpt-oss-120b,openai/gpt-oss-20b,qwen/qwen3-32b", "8192", "65536", "https://api.groq.com/openai/v1")...)
	add(providerFields(domain.ProviderMistral, "Mistral", "MISTRAL", "mistral-small-latest", "mistral-small-latest,mistral-large-latest,open-mistral-nemo", "8192", "65536", "https://api.mistral.ai/v1")...)
	add(providerFields(domain.ProviderOpenRouter, "OpenRouter", "OPENROUTER", "openrouter/free", "openrouter/free,google/gemma-4-31b-it:free,nvidia/nemotron-3-super-120b-a12b:free", "8192", "65536", "https://openrouter.ai/api/v1")...)
	effort := stringField("ai.claude_effort", "CLAUDE_EFFORT", "ai", "Effort Claude", "Для коротких сообщений хватает low",
		"low", KindSelect, selectOf(effortOptions...), func(s *domain.Settings) *string { return &s.ClaudeEffort })
	effort.Options = effortOptions
	timeout := intField("ai.timeout_seconds", "AI_TIMEOUT", "ai", "Таймаут запроса к LLM, с", "", "90", 5, 600,
		func(s *domain.Settings) *int { return &s.AITimeoutSec })
	intSet := timeout.set
	timeout.set = func(s *domain.Settings, v string) error { // env holds a duration like "90s"
		if d, err := time.ParseDuration(strings.TrimSpace(v)); err == nil {
			v = strconv.Itoa(int(d.Seconds()))
		}
		return intSet(s, v)
	}
	workers := intField("ai.workers", "ANALYSIS_WORKERS", "ai", "Параллельных анализов", "", "2", 1, 16,
		func(s *domain.Settings) *int { return &s.AnalysisWorkers })
	workers.Restart = true
	add(effort,
		boolField("ai.claude_fallbacks", "CLAUDE_FALLBACKS", "ai", "Серверный fallback Claude",
			"При отказе модели (только claude-opus-5 / claude-fable-5-1)", "true",
			func(s *domain.Settings) *bool { return &s.ClaudeFallbacks }),
		timeout,
		intField("ai.max_retries", "AI_MAX_RETRIES", "ai", "Повторов при ошибке", "", "3", 0, 10,
			func(s *domain.Settings) *int { return &s.AIMaxRetries }),
		workers,
	)

	// triage
	sens := stringField("triage.sensitivity", "SENSITIVITY", "triage", "Чувствительность",
		"Чем выше, тем больше сообщений становится задачами", "medium", KindSelect, selectOf(sensitivityOptions...),
		func(s *domain.Settings) *string { return (*string)(&s.Sensitivity) })
	sens.Options = sensitivityOptions
	add(
		sens,
		intField("triage.debounce_seconds", "DEBOUNCE_SECONDS", "triage", "Дебаунс, с",
			"Пауза тишины перед анализом пачки сообщений", "20", 1, 600,
			func(s *domain.Settings) *int { return &s.DebounceSeconds }),
		intField("triage.debounce_max_wait_seconds", "DEBOUNCE_MAX_WAIT_SECONDS", "triage", "Максимальное ожидание, с",
			"Если собеседник пишет без остановки", "120", 1, 3600,
			func(s *domain.Settings) *int { return &s.DebounceMaxWaitSeconds }),
		intField("triage.context_messages", "CONTEXT_MESSAGES", "triage", "Сообщений контекста",
			"Сколько предыдущих сообщений диалога видит модель", "12", 0, 100,
			func(s *domain.Settings) *int { return &s.ContextMessages }),
		boolField("triage.noise_prefilter", "NOISE_PREFILTER_ENABLED", "triage", "Фильтр явного шума",
			"«спасибо», «ок», эмодзи отсеиваются без вызова LLM", "true",
			func(s *domain.Settings) *bool { return &s.NoisePrefilter }),
		boolField("triage.paused", "", "triage", "⏸ Пауза личного триажа",
			"Сообщения Telegram Business сохраняются, но не анализируются. Хелпдеск не затрагивает", "false",
			func(s *domain.Settings) *bool { return &s.TriagePaused }),
		stringField("triage.owner_about", "OWNER_ABOUT", "triage", "О владельце",
			"Кто вы и чем занимаетесь — для личного триажа Telegram Business", "", KindText, maxLen(2000),
			func(s *domain.Settings) *string { return &s.OwnerAbout }),
		boolField("triage.mark_read_on_work", "MARK_READ_ON_WORK", "triage", "👁 Отмечать прочитанным",
			"При переводе личной задачи «В работу»", "true", func(s *domain.Settings) *bool { return &s.MarkReadOnWork }),
		boolField("task.auto_close_on_done", "AUTO_CLOSE_ON_DONE", "triage", "Закрывать задачу по моему «готово»",
			"Если вы сами пишете собеседнику «готово», «сделал», «готово, отключился» и т.п., открытая личная задача из этого чата закрывается; если их несколько — бот спросит, какую", "true",
			func(s *domain.Settings) *bool { return &s.AutoCloseOnDone }),
		boolField("task.notify_done_on_close", "NOTIFY_DONE_ON_CLOSE", "triage", "Спрашивать про «Готово»",
			"При закрытии задачи предлагать отправить сообщение", "false",
			func(s *domain.Settings) *bool { return &s.NotifyDoneOnClose }),
		boolField("ui.deep_links", "DEEP_LINKS", "triage", "Ссылки на исходные сообщения",
			"tg://openmessage в карточках личных задач", "true", func(s *domain.Settings) *bool { return &s.DeepLinks }),
		intField("triage.personal_reminder_minutes", "", "triage", "Повторное напоминание о личных задачах, мин",
			"Бот повторно напоминает об открытой задаче из личных сообщений, пока её не закроют. 0 — выключено",
			"0", 0, 10080, func(s *domain.Settings) *int { return &s.PersonalReminderMinutes }),
	)

	// general
	add(
		stringField("general.timezone", "TIMEZONE", "general", "Часовой пояс", "IANA, например Europe/Moscow",
			"Europe/Moscow", KindString, checkTimezone, func(s *domain.Settings) *string { return &s.Timezone }),
		boolField("digest.enabled", "DIGEST_ENABLED", "general", "🌅 Утренний дайджест", "Список висящих и просроченных задач",
			"true", func(s *domain.Settings) *bool { return &s.DigestEnabled }),
		stringField("digest.time", "DIGEST_TIME", "general", "Время дайджеста", "", "09:00", KindTime, checkClock,
			func(s *domain.Settings) *string { return &s.DigestTime }),
		stringField("webapp.public_url", "WEBAPP_PUBLIC_URL", "general", "Публичный адрес веб-панели",
			"https://… — кнопка «Открыть веб-панель» в боте", "", KindString, checkPublicURL,
			func(s *domain.Settings) *string { return &s.WebAppPublicURL }),
		intField("storage.retention_days", "MESSAGE_RETENTION_DAYS", "general", "Хранить историю сообщений, дней",
			"0 — бессрочно; задачи не удаляются", "90", 0, 3650, func(s *domain.Settings) *int { return &s.RetentionDays }),
	)

	// backup
	add(
		boolField("backup.enabled", "BACKUP_ENABLED", "backup", "Бэкапы по расписанию", "", "true",
			func(s *domain.Settings) *bool { return &s.Backup.Enabled }),
		stringField("backup.time", "BACKUP_TIME", "backup", "Время бэкапа", "", "03:30", KindTime, checkClock,
			func(s *domain.Settings) *string { return &s.Backup.Time }),
		intField("backup.keep", "BACKUP_KEEP", "backup", "Хранить бэкапов на сервере", "", "7", 1, 365,
			func(s *domain.Settings) *int { return &s.Backup.Keep }),
		boolField("backup.send_telegram", "BACKUP_SEND_TELEGRAM", "backup", "Отправлять бэкап в Telegram",
			"Файл приходит владельцу в личку с ботом", "true", func(s *domain.Settings) *bool { return &s.Backup.SendTelegram }),
	)
	return fields
}

// settingFields is the registry of every persisted setting except the AI chain.
var settingFields = buildFields()

// SettingFields returns the registry (read-only use).
func SettingFields() []SettingField { return settingFields }

func fieldByKey(key string) (SettingField, bool) {
	for _, f := range settingFields {
		if f.Key == key {
			return f, true
		}
	}
	return SettingField{}, false
}

// DefaultSettings returns built-in defaults of every field.
func DefaultSettings() domain.Settings {
	var s domain.Settings
	for _, f := range settingFields {
		if err := f.set(&s, f.Default); err != nil {
			panic("settings: bad default of " + f.Key + ": " + err.Error())
		}
	}
	return s
}
