# Многоэтапная сборка легковесного production-образа poisq
# Этап 1: Сборка статического бинарного файла на Go (с зашитым через //go:embed фронтендом)
# Builder всегда работает на нативной архитектуре раннера ($BUILDPLATFORM) и кросс-компилирует Go
# под целевую платформу — без эмуляции QEMU (она замедляет сборку arm64 в разы).
FROM --platform=$BUILDPLATFORM golang:alpine AS builder

ARG TARGETOS=linux
ARG TARGETARCH

WORKDIR /src

# Устанавливаем ca-certificates и git при необходимости
RUN apk --no-cache add ca-certificates git

# Разрешаем автоматическое управление версией тулчейна
ENV GOTOOLCHAIN=auto

# Кэшируем зависимости
COPY go.mod go.sum ./
RUN go mod download

# Копируем исходный код и встроенный веб-интерфейс (internal/admin/ui)
COPY . .

# Собираем чистый статический бинарник без CGO (pure Go modernc.org/sqlite + //go:embed)
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /bin/poisq ./cmd/proxy

# Этап 2: Финальный образ на базе Alpine Linux с утилитами аудио-анализа (ffmpeg, flac)
FROM alpine:3.20

# Устанавливаем сертификаты, часовые пояса (tzdata для PST сброса квот), ffmpeg и flac
RUN apk --no-cache add ca-certificates tzdata ffmpeg flac && \
    addgroup -S appgroup && adduser -S appuser -G appgroup

WORKDIR /app

# Копируем скомпилированный бинарный файл из builder
COPY --from=builder /bin/poisq /app/poisq

# Порт 4534 — публичный Subsonic-прокси (только умный поиск)
# Порт 4535 — закрытая веб-админка poisq Studio (локальная сеть / VPN / SSH-туннель)
EXPOSE 4534 4535

# Настройки по умолчанию через переменные окружения
ENV LISTEN_ADDR=":4534" \
    ADMIN_LISTEN_ADDR=":4535" \
    NAVIDROME_URL="http://navidrome:4533" \
    NAVIDROME_DB_PATH="/data/navidrome.db" \
    POISQ_DB_PATH="/data/poisq.db" \
    MUSIC_PATH="/music" \
    SYNC_INTERVAL="2m" \
    ENRICH_SQLITE_FULLTEXT="true" \
    LOG_LEVEL="info"

ENTRYPOINT ["/app/poisq"]
