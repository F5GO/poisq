# Справочник HTTP API

Сервис предоставляет три группы HTTP-интерфейсов на разных портах: публичный прокси (`:4534`), REST API v1 Studio (`:4535`) и внутренний HTTP API сервиса выравнивания (`:4540`).

## 1. Публичный Subsonic-прокси (`:4534`)

Все стандартные маршруты Navidrome проксируются прозрачно. Ниже перечислены перехватываемые и добавленные маршруты (`internal/proxy`, `internal/cast`):

| Метод | Путь | Описание |
|---|---|---|
| `GET`, `POST` | `/rest/search`, `/rest/search2`, `/rest/search3` *(и `.view`)* | Умный поиск с fan-out (до 4 запросов), слиянием JSON/XML и декорированием имён для клиентов `c != poisq`. |
| `GET` | `/api/search`, `/api/song*`, `/api/album*`, `/api/artist*` | Рерайт поисковых запросов веб-UI Navidrome и Feishin. |
| `GET`, `POST` | `/rest/getArtistInfo2` *(и `.view`)* | Подстановка проверенной русской биографии из `poisq.db` с on-demand ожиданием до `BIO_FIRST_REQUEST_WAIT`. |
| `GET` | `/rest/getOpenSubsonicExtensions` *(и `.view`)* | Добавляет расширение `{"name":"poisq","versions":[1]}` в ответ Navidrome. |
| `GET`, `POST` | `/rest/poisqCastZones` *(и `.view`)* | Список доступных колонок Music Assistant (требуется валидная Subsonic-авторизация). |
| `GET`, `POST` | `/rest/poisqCastPlay` *(и `.view`)* | Запуск очереди на колонке (`zone`, `id`…, опц. `offset`). |
| `GET`, `POST` | `/rest/poisqCastControl` *(и `.view`)* | Управление колонкой (`zone`, `action`, `position`, `volume`, `id`…). |
| `GET`, `POST` | `/rest/poisqCastStatus` *(и `.view`)* | Статус воспроизведения и интерполированная позиция (`zone`). |
| `GET` | `/_smart/test?q=...` | Диагностика поискового пайплайна (активен только при `ENABLE_DEBUG_API=true`). |
| `GET` | `/_smart/aliases` | Read-only список активных алиасов (активен только при `ENABLE_DEBUG_API=true`). |

---

## 2. REST API v1 — poisq Studio (`:4535`)

Все маршруты защищены `AdminGuardMiddleware` (проверка `Host` от DNS Rebinding + CSRF-проверка `Origin` для изменяющих запросов) и ограничением тела запроса `4 МиБ`.

### Состояние, синхронизация и диагностика

| Метод | Путь | Назначение |
|---|---|---|
| `GET` | `/api/v1/health` | Статистика медиатеки, статус паузы воркера, `serverTime` и проверка доступности `aligner` (таймаут `2s`). |
| `POST` | `/api/v1/scan/sync` | Немедленная синхронизация каталога треков из `navidrome.db`. |
| `POST` | `/api/v1/scan/audit` | Асинхронный (`202 Accepted`) аудит MusicBrainz и обложек: по одному `trackId` или `limitFolders` (по умолчанию `20`). |
| `POST` | `/api/v1/db/reset` | Сброс `poisq.db` (`keepKeys` по умолчанию `true`) с немедленной повторной синхронизацией из Navidrome. |
| `GET` | `/api/v1/debug/fs` | Диагностика монтирования `/music` (`?sub=`) и выборочных путей из `navidrome.db`. |

### Медиатека, медиапотоки и обложки

| Метод | Путь | Назначение |
|---|---|---|
| `GET` | `/api/v1/tracks` | Список треков: `?status=` (`all`, `synced`, `plain`, `none`, `mojibake`, `no_cover`), `?q=` (умный поиск), `?limit=` (дефолт `500`, макс. `2000`). |
| `GET` | `/api/v1/tracks/stream` | Стриминг аудио (`?id=`) с `Range`; fallback через Navidrome `/rest/stream` при настроенном служебном аккаунте. |
| `GET` | `/api/v1/tracks/waveform` | Огибающая громкости (`?id=`, 100 пар `min/max` в секунду, кэш на диске). |
| `POST` | `/api/v1/tracks/tags` | Ручное редактирование тегов трека (`trackId`, `title`, `artist`, `album`, `albumArtist`, `year`) с записью в историю Staging. |
| `GET` | `/api/v1/covers/local` | Локальная или встроенная обложка папки/трека (`?path=&album=&artist=`). |
| `GET` | `/api/v1/covers/candidates` | Поиск до 12 кандидатов обложек в Cover Art Archive и iTunes (`?artist=&album=&mbid=&path=`). |
| `GET`, `POST` | `/api/v1/covers/ai-resolve` | AI-арбитраж спорной папки (`path` или Staging `id`) с обновлением предложения. |
| `GET` | `/api/v1/navidrome/artist-image` | Фото артиста (`?artist=`) из `navidrome.db`, кэша, Navidrome или папки. |
| `GET` | `/api/v1/navidrome/album-cover` | Обложка альбома (`?album=&artist=&path=&trackId=`). |

### Тексты и очередь задач

| Метод | Путь | Назначение |
|---|---|---|
| `GET` | `/api/v1/lyrics/cache` | Данные трека (`?trackId=`), кэш вариантов провайдеров (`provider_cache`) и карта уверенности `confidence`. |
| `POST` | `/api/v1/lyrics/save` | Атомарное сохранение `.lrc` и статуса трека (`trackId`, `source`, `content`, `verified`, опц. `confidence`, `corrections`). |
| `POST`, `DELETE` | `/api/v1/lyrics/delete` | Удаление `.lrc` с диска и очистка текста, `provider_cache` и `confidence` трека. |
| `POST` | `/api/v1/lyrics/enqueue` | Постановка задачи (`trackId`, `jobType`: `tournament`, `ai_consensus`, `whisper_karaoke`, `full_pipeline`; либо `enqueueAll: true`). |
| `GET`, `POST`, `DELETE` | `/api/v1/jobs` | `GET` — последние 100 задач + живые логи (или `?trackId=` для одного трека); `POST {paused}` — пауза воркера; `DELETE ?id=` — удаление. |
| `POST`, `DELETE` | `/api/v1/jobs/action` | Действие над задачей: `id`, `action` (`pause`, `resume`, `retry`, `delete`), `stage` (`resume` или `restart`). |

### Staging-буфер (Исправления)

| Метод | Путь | Назначение |
|---|---|---|
| `GET` | `/api/v1/staging` | Список предложений (`?status=pending`, лимит `300`) с перепроверкой обложек на лету. |
| `POST` | `/api/v1/staging/apply` | Применение одного (`id`) или нескольких (`ids`) предложений; опционально `coverUrl`. |
| `POST` | `/api/v1/staging/ignore` | Отклонение предложения (`id`). |
| `POST` | `/api/v1/staging/undo` | Откат применённого предложения (`id`). |
| `POST` | `/api/v1/staging/clear-covers` | Очистка всех `pending`-предложений по обложкам. |
| `POST` | `/api/v1/staging/clear-all` | Очистка всех `pending`-предложений. |

### Биографии и настройки

| Метод | Путь | Назначение |
|---|---|---|
| `GET` | `/api/v1/artist-biographies` | Поиск артистов из `navidrome.db` (`?query=&limit=`, `1..50`). |
| `GET`, `PUT`, `DELETE` | `/api/v1/artist-biographies/detail` | `GET ?id=` — карточка и статус задачи; `PUT` — сохранение ручной русской биографии; `DELETE ?id=` — возврат к автоматической. |
| `POST` | `/api/v1/artist-biographies/reset` | Сброс автоматического кэша биографий с сохранением ручных описаний. |
| `POST` | `/api/v1/artist-biographies/rebuild` | Принудительная пересборка биографии артиста (`artistId`, `202 Accepted`). |
| `GET`, `POST`, `DELETE` | `/api/v1/keys` | Список, добавление и удаление ключей в шифрованном пуле (`?id=`). |
| `GET`, `POST` | `/api/v1/cast` | Чтение/сохранение настроек Music Assistant и живой `Discover` колонок/провайдеров. |
| `GET`, `POST` | `/api/v1/navidrome/account` | Чтение/сохранение/очистка служебной учётной записи Navidrome с проверкой `/rest/ping`. |

---

## 3. HTTP API `poisq-aligner` (`:4540`)

При заданной переменной `ALIGNER_TOKEN` все маршруты кроме `/healthz` требуют заголовок `Authorization: Bearer <token>` (сравнение через `hmac.compare_digest`).

| Метод | Путь | Описание |
|---|---|---|
| `GET` | `/healthz` | Статус сервиса (`ok`, `version: "1.0.0"`, `queue`, `running`). |
| `POST` | `/v1/jobs` | Постановка задачи выравнивания (лимит тела `2 МиБ`): `audio_path`, `lines` (`1..2000`, до 600 симв.), `lang`, `track_id`, `groq_key`, `fresh`. Идемпотентен по `track_id` для активных задач. Возвращает `202 Accepted` с `{id, status: "queued"}`. |
| `GET` | `/v1/jobs/<id>` | Состояние задачи (`queued | running | done | error | cancelled`), последние 200 записей `progress`, `error`/`error_code` и итоговый `result` (`lrc`, `lines`, `summary`). Результаты хранятся 24 часа. |
| `DELETE` | `/v1/jobs/<id>` | Отмена задачи (для `running` отправляет `SIGTERM` группе процессов). |
