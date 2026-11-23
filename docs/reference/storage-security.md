# Базы данных и безопасность

Сервис работает с двумя SQLite-базами на базе чистого Go-драйвера `modernc.org/sqlite` (без CGO) и реализует многоуровневую изоляцию файловой системы, секретов и сетевых запросов.

## Базы данных SQLite (`internal/db`)

### `navidrome.db` (только чтение)
- Открывается с прагмами `busy_timeout(5000)`, `journal_mode(WAL)`, `synchronous(NORMAL)`, `foreign_keys(OFF)` (пул: `MaxOpenConns=5`).
- Динамические имена таблиц и колонок проверяются белыми списками (`artist`, `album`, `media_file` и `id`, `name`, `title`, `full_text`, `play_count`), а структура адаптируется через `PRAGMA table_info` под разные версии схемы Navidrome.
- Используется только для чтения словаря поиска, синхронизации каталога треков, списка артистов и путей файлов для Cast. Единственная запись — одноразовая очистка `RemoveLegacyEnrichment` старой таблицы `_poisq_enrichment` при её наличии.

### `poisq.db` (собственное хранилище)
- Открывается с прагмами `busy_timeout(5000)`, `journal_mode(WAL)`, `synchronous(NORMAL)`, `foreign_keys(ON)` (пул: `MaxOpenConns=4`). Схема применяется идемпотентно в `initPoisqSchema` (`CREATE TABLE IF NOT EXISTS`).

| Таблица | Назначение |
|---|---|
| `tracks` | Индекс медиатеки: пути, метаданные, разрешение обложки, флаг `is_mojibake`, `lyrics_status` (`none`, `plain`, `synced`, `verified`, `instrumental`), активный текст и флаг синхронизации `.lrc`. |
| `staging_changes` | Очередь предложений исправлений (`mojibake`, `musicbrainz`, `cover`, `artist_canonical`, `manual`), `current_json`, `proposed_json`, слепок отката `original_tags_json` и статус (`pending`, `applied`, `ignored`, `reverted`). |
| `lyrics_jobs` | Очередь задач фонового воркера (`track_id` UNIQUE, `job_type`, `priority`, `status`, `attempt_count`, `next_retry_at`, `error_log`). |
| `provider_cache` | Кэш ответов провайдеров текстов и пословной транскрибации `whisper_words_v3` (`UNIQUE(track_id, provider)`). |
| `ai_alias_cache` | Кэш вариантов имён артистов от `LLMQueryExpander`. |
| `lrc_confidence` | Построчная карта уверенности выравнивания `poisq-aligner` и сводка `summary_json` по `track_id`. |
| `lrc_corrections` | Журнал ручных правок таймингов из редактора Studio (`auto_sec → corrected_sec`). |
| `api_keys` | Зашифрованный пул ключей (`provider`, `label`, `key_encrypted`, `key_mask`, `status`, `cooldown_until`, счётчики). |
| `settings` | Хранилище ключ-значение (конфигурация `cast` и служебные настройки). |
| `artist_bio_identity` | Подтверждённая личность артиста (MBID, источник, `confidence`, `evidence_json`, кэш 30 дней). |
| `artist_bio_candidates` | Сырые и переведённые кандидаты биографий из каталогов и `ai_summary` (`UNIQUE(artist_id, provider, source_artist_id)`). |
| `artist_biographies` | Итоговая привязка: ссылка на `automatic_candidate_id` (`ON DELETE RESTRICT`) и независимые поля `manual_text` / `manual_source_url`. |
| `artist_bio_jobs` | Состояние фоновых задач сбора биографий (`queued`, `running`, `ready`, `no_data`, `retryable`). |
| `artist_bio_web_cache` | Кэш результатов Tavily Search по `(artist_id, query_version)` с `expires_at`. |

---

## Контуры безопасности

### Шифрование ключей (`internal/security/crypto.go`)
- Все API-ключи, токены Music Assistant и пароль служебного аккаунта Navidrome шифруются **AES-256-GCM** со случайным nonce (ключ — `SHA-256` от мастер-секрета).
- Если `POISQ_SECRET_KEY` не задан, автоматически создаётся файл `.poisq_master_key` (32 байта `crypto/rand`, права `0600`, атомарное создание `O_EXCL`) в том же каталоге, что и `poisq.db`.
- В UI и API ключи возвращаются только в замаскированном виде (`MaskKey`: первые 4 и последние 4 символа).

### Защита файловой системы (`internal/security/path_guard.go`)
- `PathGuard` разрешает симлинки корня `MUSIC_PATH` и проверяет каждый целевой путь перед чтением/записью.
- Блокирует Path Traversal (`..`), висячие симлинки и выход по симлинку за пределы корня (Symlink Escape).
- Запрещает запись в скрытые файлы (начинающиеся с `.`), кроме временных `.poisq_tmp_*`.
- Ограничивает расширения для записи белым списком: `.flac`, `.mp3`, `.m4a`, `.ogg`, `.opus`, `.lrc`, `.jpg`, `.jpeg`, `.png`.

### Защита poisq Studio (`internal/security/admin_guard.go`)
- **DNS Rebinding**: `AdminGuardMiddleware` принимает запросы только с `Host` из loopback/private/link-local IP, имён без точки, локальных зон (`.localhost`, `.local`, `.lan`, `.internal`, `.home.arpa`) или явно перечисленных в `ADMIN_HOSTS`. Остальные получают `403`.
- **CSRF**: для `POST`, `PUT`, `DELETE`, `PATCH` при наличии заголовка `Origin` его хост обязан совпадать с `Host` запроса (иначе `403`).
- **Заголовки ответа**: `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Referrer-Policy: same-origin`.
- **Ограничение ввода**: лимит тела `4 МиБ` на сервере Studio, строгий JSON-декодер с запретом неизвестных и дублирующихся ключей для биографий.

### Защита от SSRF и отказоустойчивость HTTP (`internal/httpclient`, `metadata`, `artistbio`)
- **SSRF-проверки**: при скачивании обложек (`cover_fetcher.go`) и валидации ссылок веб-поиска (`web_search.go`) разрешены только публичные HTTP/HTTPS адреса; `localhost`, `.local`, `.internal`, loopback/private/link-local/multicast IP отклоняются, включая редиректы (до 5 переходов). Скрапер Genius допускает строго `https://*.genius.com`. В `provider_http.go` закрепляется origin (`RestrictOrigin`) и проверяются пути.
- **`ResilientClient`** (`internal/httpclient`): объединяет семафор с минимальным интервалом (`ProviderLimiter`), экспоненциальный retry с jitter (`150ms..3s` + `0..119ms` на сетевые ошибки, `429` и `5xx`), жёсткий лимит чтения тела (до `4 МиБ`) и **Circuit Breaker** (3 последовательных сбоя размыкают цепь на `3m` с пробным Half-Open запросом).

::: danger Сброс базы данных
Запрос `POST /api/v1/db/reset` (или кнопка «Сбросить базу» в Studio → Настройки → Обслуживание) очищает все рабочие таблицы `poisq.db` (кроме `settings` и, при `keepKeys=true`, `api_keys`), включая ручные описания биографий и историю Undo, а затем выполняет `VACUUM`. Действие необратимо.
:::
