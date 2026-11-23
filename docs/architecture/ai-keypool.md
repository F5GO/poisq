# ИИ-слой и пул API-ключей

Все обращения к внешним LLM, распознаванию речи и платным/авторизованным API централизованы в пакетах `internal/ai` и `internal/keypool`.

## Каскад LLM (`FallbackGenerator`)

В `cmd/proxy/main.go` текстовые ИИ-задачи подключаются через общий интерфейс `ai.TextGenerator` и обёртку `FallbackGenerator`:
- По умолчанию сначала используется **Groq Chat**, а при его недоступности или отсутствии ключей запрос переходит в **Google Gemini**.
- Переменная окружения `LLM_PRIMARY=gemini` меняет порядок на `Gemini → Groq`.

## Провайдеры и модели

### Groq Chat (`internal/ai/groq_chat.go`)
- **Эндпоинты**: `POST https://api.groq.com/openai/v1/chat/completions` (`temperature: 0`), автодискавери `GET /openai/v1/models` (кэш `1h`).
- **Выбор модели** (до 4 кандидатов): закреплённая `GROQ_LLM_MODEL` → предпочтения задачи через `context` (например, `openai/gpt-oss-120b` для синтеза биографий) → белый список предпочтений (`gpt-oss-120b`, `qwen3.8-27b`, `qwen3-32b`, `qwen-qwq-32b`, `llama-3.3-70b`, `llama-4-maverick`, `llama-4-scout`, `kimi-k2`), с исключением `whisper`, `guard`, `tts`, `embed`, `distil`, `8b`, `gpt-oss-20b` и др.
- **JSON-режим**: для `qwen` передаётся `reasoning_format: "parsed"`, для `gpt-oss` — `reasoning_effort: "low"`. При `400 json_validate_failed` выполняется повтор в режиме `text/plain` с усиленной системной инструкцией.
- **Отказоустойчивость**: таймаут `30s`, до 4 смен ключа (`AcquireExcluding`), пер-модельный in-memory кулдаун (`30m` при TPD/RPD, `2..30s` при минутном `429`). При TPD чат-модели ключ получает лишь 65-секундный кулдаун, а не `exhausted_daily`, чтобы не блокировать Whisper на том же ключе.

### Groq Whisper (`internal/ai/groq_whisper.go`)
- **Эндпоинт**: `POST https://api.groq.com/openai/v1/audio/transcriptions` (`verbose_json`, пословные и сегментные таймкоды, таймаут `45s`, лимит ответа `8 МиБ`).
- **Модели**: `whisper-large-v3` с автоматическим fallback на `whisper-large-v3-turbo` при `404`/`503`/`400`.
- В `prompt` передаётся только короткая строка `Артист — Название` (до 90 рун), без текста песни — чтобы модель не пропускала первый куплет.

### Google Gemini (`internal/ai/gemini_client.go`)
- **Модели**: по умолчанию `gemini-3.8-flash` (устаревшие `gemini-2.0-flash` и `gemini-2.5-flash` принудительно заменяются), каскад с fallback на `gemini-3.6-flash`.
- **Эндпоинты**: основной **Interactions API** (`POST /v1beta/interactions`, `store: false`, заголовок `Api-Revision: 2026-05-20`), при `404`/`405` — fallback на `models/{model}:generateContent` (`temperature: 0.1`).
- **Ретраи**: таймаут `90s`, до 8 попыток со сменой ключей; при коротком `503`-кулдауне (`≤ 25s`) дожидается освобождения ключа.

## Карта использования ИИ в сервисе

| Модуль `internal/ai` | Где используется | Задача |
|---|---|---|
| `lyrics_cleaner.go` | Воркер текстов | Определение языка (`langCode`), чистка встроенных переводов, романизации и структурных тегов, слияние топ-3 кандидатов турнира. |
| `karaoke_merger.go` + `groq_whisper.go` | Воркер текстов | Облачное караоке: транскрибация Opus через Whisper + выравнивание Нидлмана–Вунша (+ опциональный проход LLM при `KARAOKE_USE_GEMINI=true`). |
| `query_expander.go` | Турнир текстов | Генерация до 6 реальных вариантов написания артиста (кэшируется в `ai_alias_cache`). |
| `artist_judge.go` | Турнир текстов и `artistbio` | Проверка, относятся ли найденные записи к одному артисту (`SameArtist`) и строгий JSON-арбитраж спорной личности (`ArbitrateIdentity`). |
| `metadata_arbiter.go` | Аудит и Studio | Определение канонического артиста, альбома, типа релиза и запроса обложки для спорных папок. |
| `biography_translation.go` | Валидация биографий | Классификация языка (`ClassifyLanguage`), сегментный перевод (`Translate`) и проверка точности (`VerifyGrounding`). |
| `biography_synthesis.go` | Синтез биографий | 3-этапный конвейер `Collect → Edit → Verify` со строгим JSON-декодером и проверкой чисел/грамматики. |

## Пул API-ключей (`internal/keypool`)

Менеджер (`internal/keypool/manager.go`) хранит ключи в таблице `api_keys` базы `poisq.db` в зашифрованном виде (**AES-256-GCM**) и поддерживает 11 типов провайдеров:
`groq`, `gemini`, `yandex`, `genius`, `tavily`, `music_assistant`, `navidrome`, `acoustid`, `apple_music`, `lastfm`, `discogs`.

*(В веб-форме раздела «Ключи API» в Studio вынесены 3 основных пользовательских провайдера — `groq`, `yandex`, `tavily`; токены `music_assistant` и `navidrome` управляются на собственных вкладках настроек.)*

### Алгоритм выдачи и ротации

1. **LRU-распределение**: `Acquire` / `AcquireExcluding` предварительно размораживает ключи с истёкшим `cooldown_until`, выбирает активный ключ с самым старым `last_used_at` (избегая последнего использованного ключа при наличии альтернатив) и расшифровывает его на лету.
2. **Обработка ошибок и статусы**:

| Событие | Статус ключа | Длительность блокировки |
|---|---|---|
| Успешный запрос | `active` | Инкремент `success_count`. |
| HTTP `429` (минутный лимит) | `cooldown` | **`65s`** (`CooldownRateLimitDuration`). |
| HTTP `503` / `502` / `504` | `cooldown` | **`20s`**. |
| Исчерпание дневной квоты (RPD / `RESOURCE_EXHAUSTED`) | `exhausted_daily` | До следующей полуночи по **`America/Los_Angeles` + 5 минут** (`quota_clock.go`, ~10:05–11:05 МСК; fallback UTC-8). |
| HTTP `401` / `403` / невалидный ключ | `invalid` | Бессрочно (до удаления/замены). |
| Ошибка расшифровки AES-GCM | `decrypt_error` | До перезапуска процесса с верным `POISQ_SECRET_KEY` (сбрасывается в `NewManager`). |
