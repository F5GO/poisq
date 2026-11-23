# Установка и запуск

Сервис поставляется в виде многоплатформенных Docker-образов (`linux/amd64`, `linux/arm64`) в GitHub Container Registry и статических бинарных файлов.

## Docker Compose (рекомендуемый способ)

Базовая связка из `docker-compose.yml` запускает **Navidrome** и **poisq** с общим томом `/data` и единым путём монтирования медиатеки `/music`:

```yaml
services:
  navidrome:
    image: deluan/navidrome:latest
    container_name: navidrome
    restart: unless-stopped
    environment:
      ND_SCANSCHEDULE: "15m"
      ND_LOGLEVEL: "info"
      ND_SESSIONTIMEOUT: "24h"
      ND_BASEURL: ""
    volumes:
      - /path/to/your/music:/music:ro
      - navidrome_data:/data
    ports:
      - "4533:4533"

  poisq:
    image: ghcr.io/f5go/poisq:latest
    container_name: poisq
    restart: unless-stopped
    depends_on:
      - navidrome
    ports:
      - "4534:4534"             # Публичный Subsonic-прокси
      - "127.0.0.1:4535:4535"   # poisq Studio — только локальный доступ
    environment:
      LISTEN_ADDR: ":4534"
      ADMIN_LISTEN_ADDR: ":4535"
      NAVIDROME_URL: "http://navidrome:4533"
      NAVIDROME_DB_PATH: "/data/navidrome.db"
      POISQ_DB_PATH: "/data/poisq.db"
      MUSIC_PATH: "/music"
      SYNC_INTERVAL: "2m"
      LOG_LEVEL: "info"
    volumes:
      - navidrome_data:/data
      - /path/to/your/music:/music:rw

volumes:
  navidrome_data:
```

::: tip Права на тома
Navidrome достаточно монтировать `/music:ro`, а `poisq` нужен `/music:rw` для записи `.lrc`, `cover.jpg` и исправленных тегов. Том `/data` должен быть общим, чтобы `poisq` читал `navidrome.db` (в режиме WAL) и хранил рядом `poisq.db` и файл мастер-ключа `.poisq_master_key`.
:::

## Запуск с сервисом выравнивания `poisq-aligner`

Для точного акустического тайминга караоке и карты уверенности по строкам в `docker-compose.yml` предусмотрен отдельный профиль `aligner`:

```yaml
  aligner:
    image: ghcr.io/f5go/poisq-aligner:latest
    container_name: poisq-aligner
    profiles: ["aligner"]
    init: true
    restart: unless-stopped
    environment:
      MUSIC_ROOT: "/music"
      ALIGNER_THREADS: "6"
      # ALIGNER_TOKEN: "change-me"
    volumes:
      - /path/to/your/music:/music:ro
      - aligner_cache:/cache
```

Чтобы включить интеграцию:
1. Задайте в сервисе `poisq` переменную `ALIGNER_URL: "http://aligner:4540"` (и `ALIGNER_TOKEN`, если включена авторизация).
2. Запустите стек с профилем:

```bash
docker compose --profile aligner up -d
```

3. Добавьте хотя бы один ключ **Groq** в `poisq Studio → Настройки → Ключи API` (`poisq-aligner` использует Groq Whisper для ансамбля якорей).
4. Предусмотрите ~4 ГБ на томе `aligner_cache` для весов моделей (`htdemucs`, `Qwen3-ForcedAligner-0.6B`, `MMS_FA`) и кэша вокальных стемов.

::: warning Архитектура образа aligner
По CI-конфигурации `.github/workflows/docker-publish-aligner.yml` готовый образ `ghcr.io/f5go/poisq-aligner:latest` собирается под `linux/amd64` на базе CPU-сборки PyTorch (без требования AVX2).
:::

## Сборка из исходников

### Основной сервис (Go)

Требуется **Go 1.26.11** (версия в `go.mod`), а в рантайме — системные утилиты **`ffmpeg`** (запись тегов, транскодирование Opus, генерация waveform, извлечение встроенных обложек M4A/OGG) и **`tzdata`** (для точного расчёта полуночи `America/Los_Angeles` при сбросе квот).

```bash
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o poisq ./cmd/proxy
./poisq
```

В Docker-образе (`Dockerfile`) используется двухэтапная сборка: `golang:1.26.11-alpine` → `alpine:3.20` с установленными `ca-certificates`, `tzdata`, `ffmpeg` и `flac`.

### Запуск `poisq-aligner` локально

`aligner/Dockerfile` ставит Python 3.12, `ffmpeg`, CPU-версии `torch`/`torchaudio`, `numpy`, `demucs`, `soundfile`, `qwen-asr` и `requests`. Локальный запуск сервера:

```bash
MUSIC_ROOT=/path/to/music ALIGNER_CACHE_DIR=/tmp/aligner-cache python aligner/server.py
```

## Первоначальная настройка после старта

1. Подключите музыкальные клиенты к адресу `poisq` на порту **`:4534`** вместо прямого порта Navidrome `:4533`.
2. Откройте **poisq Studio** на `http://127.0.0.1:4535` (если доступ идёт через обратный прокси по внешнему домену — перечислите его в `ADMIN_HOSTS`).
3. В разделе **Настройки → Ключи API** добавьте нужные ключи:
   - **Groq** — для ИИ-караоке, очистки текстов, арбитража обложек и синтеза биографий.
   - **Яндекс Музыка** — OAuth-токен для синхронизированных текстов и карточек артистов.
   - **Tavily** — для веб-поиска источников биографий.
4. При необходимости настройте в **Настройки → Navidrome** служебную учётную запись (без прав администратора) для запасного стриминга аудио и подгрузки обложек из Navidrome в Studio, а в **Настройки → Music Assistant** — трансляцию на колонки.
