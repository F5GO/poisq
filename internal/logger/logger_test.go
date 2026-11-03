// Package logger реализует централизованное многоуровневое логирование для сервиса poisq.
//
// Данный файл logger_test.go тестирует корректность фильтрации сообщений по уровням (DEBUG, INFO, WARN, ERROR).
package logger

import (
	"bytes"
	"strings"
	"testing"
)

func TestLoggerLevels(t *testing.T) {
	buf := &bytes.Buffer{}
	outputWriter = buf

	// 1. Уровень INFO: DEBUG не должен писаться в лог
	SetLevel("info")
	Debug("test debug message that should be ignored")
	Info("test info message that must appear")

	out := buf.String()
	if strings.Contains(out, "test debug message") {
		t.Errorf("DEBUG message should be suppressed on INFO level")
	}
	if !strings.Contains(out, "test info message") {
		t.Errorf("INFO message should appear on INFO level")
	}

	// 2. Уровень DEBUG: оба сообщения должны быть в логе
	buf.Reset()
	SetLevel("debug")
	Debug("visible debug message %d", 42)
	Info("visible info message")

	out = buf.String()
	if !strings.Contains(out, "visible debug message 42") {
		t.Errorf("DEBUG message must appear on DEBUG level")
	}
	if !strings.Contains(out, "visible info message") {
		t.Errorf("INFO message must appear on DEBUG level")
	}
}
