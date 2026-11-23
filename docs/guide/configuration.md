# Конфигурация

Все параметры считываются из переменных окружения при старте процесса. Ниже перечислены только переменные, реально используемые в коде `poisq` и `poisq-aligner`.

## Основные переменные `poisq`

Загружаются в `internal/config/config.go`:

| Переменная | По умолчанию | Описание |
|---|---|---|
| `LISTEN_ADDR` | `:4534` | Адрес и порт публичного Subsonic-прокси. |
| `ADMIN_LISTEN_ADDR` | `:4535` | Адрес и порт веб-панели **poisq Studio**. |
| `NAVIDROME_URL` | `http://navidrome:4533` | URL вышестоящего Navidrome (`http://` или `https://`). |
| `NAVIDROME_DB_PATH` | `/data/navidrome.db` | Путь к SQLite-базе Navidrome (открывается для чтения). |
| `POISQ_DB_PATH` | `/data/poisq.db` | Путь к собственной SQLite-базе `poisq`. |
| `MUSIC_PATH` | `/music` | Корень медиатеки для `PathGuard`, сканера, LRC и теггера. |
| `POISQ_SECRET_KEY` | *(пусто)* | Мастер-ключ шифрования `api_keys` (AES-256-GCM). Если не задан, автоматически создаётся файл `.poisq_master_key` (`0600`) рядом с `POISQ_DB_PATH`. |
| `SYNC_INTERVAL` | `2m` | Периодичность синхронизации словаря и каталога треков с `navidrome.db`. |
| `TRUSTED_PROXIES` | `127.0.0.0/8,::1/128,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,fc00::/7` | CIDR/IP обратных прокси, чьим заголовкам `X-Forwarded-*`, `CF-*` и `Remote-User` доверяет сервер. Значение `none` отключает доверие всем. |
| `LOG_LEVEL` | `info` | Уровень логирования (`debug`, `info`, `warn`, `error`). |
| `ALIASES_FILE` | `/data/aliases.json` | Локальный файл пользовательских алиасов (имеет высший приоритет над встроенными и удалёнными). |
| `ALIASES_SYNC_URL` | `https://raw.githubusercontent.com/F5GO/poisq/main/aliases.json` | URL для ежедневной синхронизации алиасов. |
| `ENABLE_DEBUG_API` | `false` | Включает диагностические маршруты `/_smart/test` и `/_smart/aliases` на порту `:4534`. |
| `BIO_FIRST_REQUEST_WAIT` | `25s` | Сколько запрос `getArtistInfo2` ждёт готовности фоновой сборки биографии при первом обращении. Допустимый диапазон: `5s..30s`. |
| `ALIGNER_URL` | *(пусто)* | Базовый URL сервиса `poisq-aligner` (например, `http://aligner:4540`). Если пусто, караоке работает в облачном режиме. |
| `ALIGNER_TOKEN` | *(пусто)* | Bearer-токен для авторизации запросов к `poisq-aligner`. |
| `ALIGNER_MUSIC_PATH` | *(пусто)* | Корень медиатеки внутри контейнера `poisq-aligner`, если путь отличается от `MUSIC_PATH`. |

## Дополнительные переменные модулей `poisq`

Считываются напрямую соответствующими пакетами:

| Переменная | Где читается | По умолчанию | Описание |
|---|---|---|---|
| `ADMIN_HOSTS` | `internal/security/admin_guard.go` | *(пусто)* | Список дополнительных разрешённых `Host` для доступа к Studio через запятую (защита от DNS Rebinding). |
| `LLM_PRIMARY` | `cmd/proxy/main.go` | `groq` | Основной LLM-провайдер в `FallbackGenerator`: по умолчанию `groq` → `gemini`; значение `gemini` меняет порядок на `gemini` → `groq`. |
| `GROQ_LLM_MODEL` | `internal/ai/groq_chat.go` | *(автовыбор)* | Жёстко закрепляет модель чата Groq вместо автодискавери через `/openai/v1/models`. |
| `GEMINI_MODEL` | `internal/ai/gemini_client.go` | `gemini-3.8-flash` | Переопределяет модель Gemini (устаревшие `gemini-2.0-flash` и `gemini-2.5-flash` принудительно заменяются на `gemini-3.8-flash`). |
| `KARAOKE_USE_GEMINI` | `internal/ai/karaoke_merger.go` | `false` | При значении `true` включает дополнительный проход LLM поверх DP-выравнивания Нидлмана–Вунша в облачном караоке. |
| `LYRICS_YANDEX_TOKEN` / `YANDEX_MUSIC_TOKEN` | `internal/lyrics/tournament.go` | *(пусто)* | Запасной OAuth-токен Яндекс Музыки из окружения, если ключ `yandex` не добавлен в пул Studio. |

## Переменные `poisq-aligner`

Считываются в `aligner/server.py` и `aligner/runner.py`:

| Переменная | По умолчанию | Описание |
|---|---|---|
| `ALIGNER_LISTEN` | `0.0.0.0` | Интерфейс прослушивания HTTP-сервера. |
| `ALIGNER_PORT` | `4540` | Порт HTTP-сервера. |
| `ALIGNER_TOKEN` | *(пусто)* | Если задан, все маршруты кроме `/healthz` требуют `Authorization: Bearer <token>`. |
| `MUSIC_ROOT` | `/music` | Корневая папка медиатеки (валидация `realpath`, защита от выхода за пределы корня). |
| `ALIGNER_CACHE_DIR` | `/cache` | Каталог кэша: модели (`HF_HOME`, `TORCH_HOME`), сохранённые стемы/ASR и JSON-результаты задач. |
| `ALIGNER_THREADS` | `0` | Лимит потоков CPU (`OMP_NUM_THREADS`, `MKL_NUM_THREADS`). `0` — без явного ограничения. |
| `ALIGNER_KEEP_WORKDIRS` | `30` | Сколько последних рабочих каталогов треков (с `track_vocals16k.wav` и `asr_*.json`) хранить в кэше. |
| `ALIGNER_LEAD` | `0.5` | Упреждение таймкодов LRC в секундах (`max(0, start - lead)`). |
| `ALIGNER_MAX_SEC` | `1500` | Максимальная длительность обрабатываемого трека в секундах (25 минут). |
| `ALIGNER_JOB_TIMEOUT` | `5400` | Жёсткий таймаут одной задачи выравнивания в секундах (90 минут). |
| `ALIGNER_ASR_VARIANTS` | `vocals,prompt,mix,chunks,turbo,fast` | Набор прогонов Groq Whisper для построения медианного якоря. |
| `ALIGNER_DEMUCS_DEVICE` | *(авто)* | Устройство для Demucs (`""` — авто cuda/cpu, `cpu`, `mps`). |
| `ALIGNER_QWEN_DEVICE` | `cpu` | Устройство для `Qwen3-ForcedAligner-0.6B` (`cpu` или `mps`). |
| `GROQ_API_KEY` | *(пусто)* | Резервный ключ Groq, если он не передан в теле `POST /v1/jobs`. |
