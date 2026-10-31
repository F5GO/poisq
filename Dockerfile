# Многоэтапная сборка легковесного production-образа poisq (< 20MB)
# Этап 1: Сборка статического бинарного файла на Go
FROM golang:alpine AS builder

WORKDIR /src

# Устанавливаем ca-certificates и git при необходимости
RUN apk --no-cache add ca-certificates git

# Разрешаем автоматическое управление версией тулчейна
ENV GOTOOLCHAIN=auto

# Кэшируем зависимости
COPY go.mod go.sum ./
RUN go mod download

# Копируем исходный код
COPY . .

# Собираем чистый статический бинарник без CGO (pure Go modernc.org/sqlite)
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /bin/poisq ./cmd/proxy

# Этап 2: Минимальный финальный образ на базе Alpine Linux
FROM alpine:3.20

# Устанавливаем корневые сертификаты для внешних TLS-соединений и tzdata
RUN apk --no-cache add ca-certificates tzdata && \
    addgroup -S appgroup && adduser -S appuser -G appgroup

WORKDIR /app

# Копируем скомпилированный бинарный файл из builder
COPY --from=builder /bin/poisq /app/poisq

# Проксирует запросы на порту 4534
EXPOSE 4534

# Настройки по умолчанию через переменные окружения
ENV LISTEN_ADDR=":4534" \
    NAVIDROME_URL="http://navidrome:4533" \
    NAVIDROME_DB_PATH="/data/navidrome.db" \
    SYNC_INTERVAL="2m" \
    ENRICH_SQLITE_FULLTEXT="true" \
    LOG_LEVEL="info"

ENTRYPOINT ["/app/poisq"]
