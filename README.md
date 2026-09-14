# TG Triage — персональный AI-ассистент и таск-менеджер на Telegram Business

[![CI](https://github.com/maki072/TGTriage/actions/workflows/ci.yml/badge.svg)](https://github.com/maki072/TGTriage/actions/workflows/ci.yml)
[![Release](https://github.com/maki072/TGTriage/actions/workflows/release.yml/badge.svg)](https://github.com/maki072/TGTriage/actions/workflows/release.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go)](go.mod)

Сервис подключается к личному аккаунту владельца через **Telegram Business**, в реальном времени анализирует
входящие личные сообщения с помощью LLM (Claude, Gemini, Groq, Mistral или OpenRouter), отсекает бытовой шум, превращает запросы собеседников
в задачи с черновиком ответа и даёт управлять всем этим из приватного чата с ботом.

- Go 1.26, чистая архитектура `domain → service → repository / delivery`
- Одна внешняя зависимость — pure-Go SQLite (`modernc.org/sqlite`, режим WAL), статический бинарник без CGO
- Собственный минимальный клиент Bot API и адаптеры LLM на `net/http`

## Возможности

| Блок | Что умеет |
|---|---|
| Приём сообщений | `business_message`, правки и удаления сообщений, сообщения владельца идут в контекст, идемпотентное сохранение |
| Дебаунс | анализ после N секунд тишины в чате + жёсткий предел ожидания; буфер переживает рестарт (непроанализированные сообщения поднимаются из БД) |
| Триаж | строгий JSON по схеме: тип, анализ, `is_task`, уверенность, заголовок, описание, приоритет, категория, дедлайн, стратегия и черновик ответа; порог уверенности зависит от чувствительности; дополнение уже открытой задачи вместо дубля |
| AI | интерфейс `ai.Provider`, адаптеры Claude (Messages API, `output_config.format`), Gemini (`generateContent`, `responseJsonSchema`) и общий OpenAI-совместимый адаптер (`response_format: json_schema`) для Groq/Mistral/OpenRouter; горячее переключение провайдера и моделей из бота; ретраи с backoff, повтор при невалидном JSON |
| Таск-менеджер | меню, список с пагинацией и фильтрами по статусу/приоритету, карточка задачи со ссылками на профиль и исходное сообщение |
| Кнопки задачи | 🚀 Ответить черновиком · ✏️ Свой ответ · 👀 В работу (+ отметка прочитанным) · ✅ Закрыть (опционально — спросить и отправить «Готово!») · 🗑 Ошибка (false positive) · ⏰ Отложить (пресеты и свой срок) |
| Настройки | провайдер, модели (пресеты или ввод), дебаунс, чувствительность, дайджест, пауза триажа, вопрос про «Готово!» при закрытии, проверка провайдера, сброс к `.env` |
| Автоматизация | утренний дайджест просроченных / горящих / зависших задач, напоминания об отложенных, очистка старой истории |
| Качество | журнал всех LLM-вызовов (`analyses`), статистика: шум, ошибки, задержка, точность по ложным срабатываниям |

## Структура проекта

```
.
├── cmd/tgtriage/main.go              # точка входа, сборка зависимостей, graceful shutdown
├── internal/
│   ├── domain/                        # сущности, value objects, порты репозиториев
│   │   ├── task.go  message.go  analysis.go  settings.go  repository.go  errors.go
│   ├── ai/                            # провайдер-агностичный слой LLM
│   │   ├── provider.go                # интерфейс Provider, Registry, нормализованные ошибки
│   │   ├── prompt.go                  # системный промпт, user-промпт, JSON-схема ответа
│   │   ├── parse.go                   # извлечение/валидация JSON, разбор дедлайна
│   │   ├── claude/claude.go           # адаптер Anthropic Claude
│   │   ├── gemini/gemini.go           # адаптер Google Gemini
│   │   └── openaicompat/               # общий адаптер для Groq/Mistral/OpenRouter (один протокол)
│   ├── service/                       # use cases
│   │   ├── triage.go                  # дебаунс, очередь, воркеры, анализ, создание/дополнение задач
│   │   ├── tasks.go                   # статусы, snooze, ответы через Business API, дайджест, статистика
│   │   ├── settings.go                # runtime-настройки (env по умолчанию + переопределения в БД)
│   │   ├── connections.go             # Business-подключение владельца
│   │   ├── scheduler.go               # напоминания, дайджест, retention
│   │   └── timeparse.go               # разбор «2h», «завтра 10:00», «20.09 12:00»
│   ├── repository/sqlite/             # SQLite (WAL), миграции, репозитории
│   ├── telegram/                      # минимальный клиент Bot API + Business, long polling
│   ├── delivery/telegram/             # обработчики апдейтов, UI бота, уведомления
│   └── config/config.go               # загрузка и валидация env
├── deploy/
│   ├── systemd/tg-triage.service   # юнит systemd с hardening
│   └── install.sh                     # установка/обновление на Debian
├── .env.example
└── Makefile
```

## Как работает конвейер

```
business_message ──► сохранение в SQLite (analyzed=0)
                     └─► буфер чата: таймер DEBOUNCE_SECONDS (сбрасывается новым сообщением,
                         но не дольше DEBOUNCE_MAX_WAIT_SECONDS)
                                  │
                                  ▼
                  очередь ─► воркер (по одному батчу на чат одновременно)
                                  │  контекст: последние CONTEXT_MESSAGES сообщений диалога
                                  │  + открытые задачи с этим собеседником
                                  ▼
                  активный провайдер (из настроек, в момент вызова) ─► строгий JSON
                                  │
             ┌────────────────────┼──────────────────────────┐
     шум / уверенность      update_task_id > 0          новая задача
     ниже порога            → задача дополняется        → карточка владельцу
     → только журнал          и присылается заново        с кнопками
```

Каждый вызов LLM пишется в таблицу `analyses` (вход, сырой ответ, провайдер, модель, задержка, ошибка, связанная задача).
Кнопка «🗑 Ошибка» переводит задачу в `false_positive`, связь с анализом сохраняется — по этим записям удобно дорабатывать промпт.

## Системный промпт

Шаблон — [`internal/ai/prompt.go`](internal/ai/prompt.go) (`systemPromptTemplate`), JSON-схема — функция `AnalysisSchema()`
в том же файле. Схема одна на всех провайдеров и навязывается структурно через нативный механизм каждого
(`output_config.format` у Claude, `responseJsonSchema` у Gemini, `response_format: json_schema` у OpenAI-совместимых) —
текстом в промпт не дублируется, это экономит ~500–700 токенов на вызов. Формат ответа:

```json
{
  "message_type": "bug",
  "analysis": "Собеседник сообщает о 500 при входе в админку и просит посмотреть сегодня.",
  "is_task": true,
  "confidence": 0.93,
  "update_task_id": 0,
  "title": "Починить 500 при входе в админку",
  "description": "После вчерашнего релиза вход в админку отдаёт 500, жалуются клиенты. Нужно разобраться до конца дня.",
  "priority": "high",
  "category": "bug",
  "deadline": "2026-09-14T23:59",
  "reply_strategy": "confirm",
  "draft_reply": "Привет! Понял, сейчас посмотрю, что с входом."
}
```

Чувствительность меняет инструкцию в промпте и порог уверенности: низкая — 0.75, средняя — 0.55, высокая — 0.35.
Тексты сообщений явно помечены как данные: инструкции внутри переписки модель выполнять не должна.

## Подготовка Telegram

1. Создайте бота у [@BotFather](https://t.me/BotFather) и получите токен.
2. В BotFather: **Bot Settings → Business Mode → Turn on**.
3. Узнайте свой user id (например, у @userinfobot) — это `OWNER_ID`.
4. Запустите сервис и напишите боту `/start` (боты не могут писать первыми).
5. В приложении Telegram: **Настройки → Telegram Business → Чат-боты** → укажите бота, выберите чаты
   и включите права **«Отвечать на сообщения»** (нужно для кнопок ответа) и **«Читать сообщения»** (для отметки прочитанным).
   Для подключения чат-ботов к аккаунту нужен Telegram Premium.

Бот обрабатывает Business-подключение и команды только от `OWNER_ID`; всё остальное игнорируется.

## Конфигурация

Все параметры с комментариями — в [`.env.example`](.env.example). Минимально нужны `TELEGRAM_BOT_TOKEN`, `OWNER_ID`
и ключ хотя бы одного провайдера — `ANTHROPIC_API_KEY`, `GEMINI_API_KEY`, `GROQ_API_KEY`, `MISTRAL_API_KEY` и/или
`OPENROUTER_API_KEY`. Три последних бесплатны, без карты (console.groq.com, console.mistral.ai, openrouter.ai/keys) —
подробности и оговорки по приватности/лимитам см. в `.env.example`.

Значения из окружения — это **значения по умолчанию**. Всё, что изменено в меню «⚙️ Настройки» (провайдер, модели, дебаунс,
чувствительность, дайджест, пауза), хранится в БД и применяется сразу, без перезапуска. Кнопка «♻️ Сброс к .env» удаляет эти переопределения.

Модели по умолчанию: Claude — `claude-opus-5` с `CLAUDE_EFFORT=low` (для короткой классификации этого хватает,
и так быстрее и дешевле); Gemini — `gemini-3.6-flash`. Любую другую модель можно указать в env или ввести в боте.
Для `claude-opus-5` / `claude-fable-5-1` по умолчанию включён серверный fallback при отказе модели (`CLAUDE_FALLBACKS`).

## Сборка

Нужен Go 1.26+.

```bash
make test          # юнит-тесты (с -race; на Windows без CGO: go test ./...)
make build-linux   # статический бинарник bin/tg-triage-linux-amd64
```

Без make:

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -tags netgo,osusergo \
  -ldflags "-s -w -buildid= -X main.version=1.0.0" -o bin/tg-triage-linux-amd64 ./cmd/tgtriage
```

Проверка на сервере: `file tg-triage-linux-amd64` → `statically linked`.

Локальный запуск: `cp .env.example .env`, заполните значения и укажите `DB_PATH=./data/tgtriage.db`, затем `make run`.

## Развёртывание на Debian

Автоматически (из каталога проекта на сервере):

```bash
sudo ./deploy/install.sh bin/tg-triage-linux-amd64
sudo nano /etc/tg-triage/tg-triage.env
sudo systemctl restart tg-triage
```

Вручную:

```bash
sudo useradd --system --no-create-home --home-dir /var/lib/tg-triage --shell /usr/sbin/nologin tgtriage
sudo install -m 0755 bin/tg-triage-linux-amd64 /usr/local/bin/tg-triage
sudo install -d -m 0750 -o root -g tgtriage /etc/tg-triage
sudo install -m 0640 -o root -g tgtriage .env.example /etc/tg-triage/tg-triage.env
sudo install -m 0644 deploy/systemd/tg-triage.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now tg-triage
```

Юнит запускает сервис от непривилегированного пользователя, создаёт `/var/lib/tg-triage` через `StateDirectory`
и ограничивает процесс (`ProtectSystem=strict`, `NoNewPrivileges`, фильтр системных вызовов и т. д.).
Файл окружения с секретами доступен только root и группе сервиса.

Эксплуатация:

```bash
journalctl -u tg-triage -f                   # логи (JSON)
systemctl status tg-triage
sudo systemctl restart tg-triage             # после обновления бинарника
sqlite3 /var/lib/tg-triage/tgtriage.db ".backup '/root/tgtriage-$(date +%F).db'"   # горячий бэкап
```

При остановке (`SIGTERM`) сервис прекращает приём апдейтов и до 25 секунд ждёт завершения текущих анализов.
Сообщения, которые не успели проанализировать, остаются в БД и обрабатываются после следующего запуска.

## Ограничения и заметки

- **Реакции.** Bot API не позволяет ставить реакции в чатах, доступных через Business-подключение,
  поэтому «👀 В работу» вместо реакции отмечает исходное сообщение прочитанным (`readBusinessMessage`, отключается настройкой).
- **Ссылка на сообщение.** В личных чатах нет публичных ссылок на сообщения. Карточка содержит ссылку на профиль
  (`t.me/username` или `tg://user?id=`) и `tg://openmessage?user_id=…&message_id=…` — последнюю открывают не все клиенты.
  Если Telegram отклонит ссылку, карточка автоматически уйдёт без ссылок; отключить их совсем — `DEEP_LINKS=false`.
- **Сообщения без текста** (фото, голосовые, файлы) передаются модели как описание вида `[голосовое сообщение 12 c]` плюс подпись;
  содержимое медиа не распознаётся.
- **Приватность.** Тексты переписки уходят выбранному LLM-провайдеру. История сообщений хранится `MESSAGE_RETENTION_DAYS` дней, задачи — бессрочно.
- Long polling: вебхук при старте снимается. Запускайте только один экземпляр сервиса на токен.

## Готовые сборки

Каждый пуш в `main` собирается и тестируется через [GitHub Actions](.github/workflows/ci.yml). Каждый тег вида
`v*` дополнительно собирает статический линукс-бинарник и инсталлятор-архив (`bin/` + `deploy/` + `.env.example`,
та же раскладка, что и в репозитории) и публикует их в [Releases](../../releases) — см.
[`.github/workflows/release.yml`](.github/workflows/release.yml). Скачайте и распакуйте архив последнего
релиза, затем внутри распакованной папки: `sudo ./deploy/install.sh` — он сам создаст пользователя, каталоги
и юнит (см. «Развёртывание на Debian» выше).

## Лицензия

[MIT](LICENSE) © maki072. Используйте, форкайте, меняйте под себя — при распространении сохраняйте файл
`LICENSE` с указанием авторства.

## Отказ от ответственности

Проект не аффилирован с Telegram и использует официальный Bot API / Business API по документации. Токены,
ключи API и переписка — ответственность того, кто разворачивает сервис; ничего из этого не должно попадать
в git (см. `.gitignore`) или репозиторий.
