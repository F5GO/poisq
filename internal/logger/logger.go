// Package logger реализует централизованное многоуровневое логирование для сервиса poisq
// с поддержкой уровней DEBUG, INFO, WARN, ERROR, цветовой дифференциацией в терминале,
// фильтрацией по переменной окружения LOG_LEVEL и детальной трассировкой операций с медиатекой.
package logger

import (
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

// Level определяет уровень важности сообщения лога.
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

var (
	currentLevel = LevelInfo
	mu           sync.RWMutex
	outputWriter io.Writer = os.Stderr
)

// SetLevel устанавливает текущий порог логирования на основе строки ("debug", "info", "warn", "error").
func SetLevel(levelStr string) {
	mu.Lock()
	defer mu.Unlock()

	switch strings.ToLower(strings.TrimSpace(levelStr)) {
	case "debug", "trace", "all":
		currentLevel = LevelDebug
	case "warn", "warning":
		currentLevel = LevelWarn
	case "error", "err", "fatal":
		currentLevel = LevelError
	default:
		currentLevel = LevelInfo
	}
}

// CurrentLevelStr возвращает текущий уровень логирования в виде строки.
func CurrentLevelStr() string {
	mu.RLock()
	defer mu.RUnlock()
	switch currentLevel {
	case LevelDebug:
		return "DEBUG"
	case LevelWarn:
		return "WARN"
	case LevelError:
		return "ERROR"
	default:
		return "INFO"
	}
}

// Debug выводит отладочное сообщение (пути к файлам, поиск обложек, параметры запросов).
func Debug(format string, v ...any) {
	logWithLevel(LevelDebug, "DEBUG", format, v...)
}

// Info выводит информационное сообщение о штатных операциях сервиса.
func Info(format string, v ...any) {
	logWithLevel(LevelInfo, "INFO", format, v...)
}

// Warn выводит предупреждение о некритичных сбоях или фоллбэках.
func Warn(format string, v ...any) {
	logWithLevel(LevelWarn, "WARN", format, v...)
}

// Error выводит сообщение об ошибке.
func Error(format string, v ...any) {
	logWithLevel(LevelError, "ERROR", format, v...)
}

func logWithLevel(lvl Level, prefix string, format string, v ...any) {
	mu.RLock()
	activeLevel := currentLevel
	w := outputWriter
	mu.RUnlock()

	if lvl < activeLevel {
		return
	}

	timestamp := time.Now().Format("2006/01/02 15:04:05")
	msg := fmt.Sprintf(format, v...)
	line := fmt.Sprintf("%s [%s] %s\n", timestamp, prefix, msg)

	if w != nil {
		_, _ = io.WriteString(w, line)
	} else {
		log.Print(line)
	}
}
