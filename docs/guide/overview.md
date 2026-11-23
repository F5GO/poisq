# Обзор и архитектура

`poisq` — это сервис-компаньон для музыкального сервера **Navidrome**. Он работает как единый статический Go-бинарник (Go 1.26, без CGO, со встроенным веб-интерфейсом через `//go:embed`) и при необходимости подключает Python sidecar `poisq-aligner` для акустического выравнивания текстов.

## Разделение контуров

При запуске (`cmd/proxy/main.go`) сервис поднимает два изолированных HTTP-сервера с разными задачами и моделью доступа:

| Контур | Порт по умолчанию | Назначение | Кто подключается |
|---|---|---|---|
| **Subsonic-прокси** | `:4534` | Проксирование к Navidrome, умный поиск, отдача русских биографий в `getArtistInfo2`, объявление OpenSubsonic-расширения `poisq`, эндпоинты `poisqCast*`. | Музыкальные клиенты (iOS `Poisq`, Arpeggi, Amperfy, Feishin, веб-UI Navidrome). |
| **poisq Studio** | `:4535` | Веб-панель и REST API v1: каталог треков, караоке-плеер, редактор таймингов, буфер правок тегов и обложек, редактор биографий, пул API-ключей, настройки Cast и Navidrome. | Только локальная сеть, `localhost`, VPN или явно разрешённые `ADMIN_HOSTS`. |
| **poisq-aligner** *(отдельный контейнер)* | `:4540` | Акустическое выравнивание строк `.lrc` по вокальной дорожке и расчёт уверенности по каждой строке. | Внутренний HTTP-клиент фонового воркера `poisq`. |

Тексты песен клиентам отдаёт сам **Navidrome** — `poisq` записывает готовые `.lrc` файлы атомарно рядом с аудиофайлами в `/music`, после чего Navidrome подхватывает их при сканировании.

## Схема взаимодействия компонентов

```
  Музыкальные клиенты                   Браузер администратора
          │                                       │
          ▼ :4534 (Proxy)                         ▼ :4535 (Studio + REST API v1)
  ┌──────────────────────────────────────────────────────────────────────┐
  │                                poisq                                 │
  │                                                                      │
  │  internal/proxy   ◄──► internal/search (Trie, транслит, алиасы)      │
  │        │          ◄──► internal/cast   ──► Music Assistant (:8095)   │
  │        │          ◄──► internal/artistbio ──► 7 каталогов + Tavily   │
  │        │                                                             │
  │  internal/worker  ──► internal/lyrics  ──► LRCLIB/Яндекс/NetEase/Genius
  │        │          ──► internal/ai      ──► Groq (Whisper/LLM), Gemini│
  │        │          ──► internal/aligner ──► poisq-aligner (:4540)     │
  │        │                                                             │
  │  internal/metadata + internal/tagger (ffmpeg, MusicBrainz, iTunes)   │
  └──────────┬──────────────────────┬───────────────────────┬────────────┘
             │ HTTP                 │ SQLite (WAL)          │ Read / Write
             ▼                      ▼                       ▼
     Navidrome (:4533)     /data/navidrome.db (RO)       /music
                           /data/poisq.db     (RW)    (.lrc, cover.jpg, теги)
```

## Принцип работы с данными

- **`/data/navidrome.db`** используется только для чтения: сервис вычитывает словарь исполнителей, альбомов и треков для быстрого поиска (`db.LoadLibraryVocabulary`), синхронизирует список треков (`metadata.LibraryScanner.SyncFromNavidromeDB`), проверяет наличие артистов для биографий и получает пути файлов для Cast. При старте один раз запускается `db.RemoveLegacyEnrichment`, очищающий устаревшую таблицу `_poisq_enrichment` и старые вставки `[poisq:`/`[ndss:`, если они остались от прошлых версий.
- **`/data/poisq.db`** — собственная база SQLite (WAL, `foreign_keys=ON`), в которой хранятся индекс треков, очередь задач `lyrics_jobs`, кэш провайдеров, Staging-предложения, шифрованные API-ключи, уверенность выравнивания, настройки Cast и вся подсистема биографий.
- **`/music`** — общий том с аудиофайлами. Запись (`.lrc`, `cover.jpg`, перезапись тегов) всегда проходит через `security.PathGuard` и выполняется через временный файл `.poisq_tmp_*` с атомарным `os.Rename`.

## Фоновые процессы при старте

После запуска обоих HTTP-серверов `main.go` поднимает четыре фоновых контура, управляемых общим контекстом завершения (`SIGINT`/`SIGTERM`, graceful shutdown до `10s`):

1. **Воркер очереди текстов (`worker.QueueProcessor`)** — раз в `4s` (или мгновенно по сигналу `Wake()`) берёт задачи поиска текстов и выравнивания караоке.
2. **Синхронизация алиасов (`search.AliasManager`)** — через `5s` после старта и далее раз в `24h` подтягивает `aliases.json` из `ALIASES_SYNC_URL`.
3. **Координатор биографий (`artistbio.Coordinator`)** — последовательно обрабатывает очередь запросов на сбор и синтез русских биографий.
4. **Синхронизация с Navidrome (`runBackgroundSync`)** — сразу после старта и далее каждые `SYNC_INTERVAL` (по умолчанию `2m`) перестраивает поисковый словарь в памяти и обновляет каталог треков в `poisq.db`.

## Структура репозитория

| Каталог | Назначение |
|---|---|
| `cmd/proxy` | Точка входа сервиса, сборка зависимостей и жизненный цикл серверов. |
| `internal/proxy`, `internal/search` | Subsonic/Native-прокси, многоуровневый поисковый движок, fan-out и слияние ответов. |
| `internal/lyrics`, `internal/aligner` | Провайдеры текстов, турнир, консенсус, LRC-парсер, клиент `poisq-aligner`. |
| `internal/artistbio` | Идентификация артистов, 7 каталожных источников, веб-поиск, валидация и синтез биографий. |
| `internal/ai`, `internal/keypool` | Клиенты Groq (Chat/Whisper) и Gemini, выравнивание Нидлмана–Вунша, пул ключей с LRU и квотами. |
| `internal/metadata`, `internal/tagger` | Сканер папок, MusicBrainz, подбор и фильтрация обложек, CP1251 mojibake, атомарный теггер на `ffmpeg`. |
| `internal/worker`, `internal/db` | Очередь фоновых задач, телеметрия, работа с `poisq.db` и `navidrome.db`. |
| `internal/cast` | Интеграция с Music Assistant и Subsonic-эндпоинты `poisqCast*`. |
| `internal/admin` | HTTP-сервер и встроенный SPA-интерфейс **poisq Studio** (`internal/admin/ui`). |
| `internal/security`, `internal/cache`, `internal/httpclient` | Шифрование AES-256-GCM, `PathGuard`, `AdminGuard`, кэш/singleflight, circuit breaker и rate limiter. |
| `aligner/` | Python-сервис `poisq-aligner` (HTTP API, запуск `aligneval.py`, расчёт уверенности). |
| `tools/aligneval/` | CLI-стенд оценки качества выравнивания и построения консенсусного эталона. |
| `chrome-extension/` | Расширение Chrome MV3 для получения OAuth-токена Яндекс Музыки. |
| `app/ios/` | Нативный iOS-клиент `Poisq` (SwiftUI, `PoisqKit`, виджеты, CarPlay, Siri). |
