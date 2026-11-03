// Package ai реализует облачные AI-пайплайны сервиса poisq: мультиязычную очистку
// и консенсус текстов через Gemini 2.5 Flash, сжатие аудио на лету в моно-Opus
// и двухэтапную генерацию синхронизированного караоке через Groq Whisper + Gemini.
//
// Данный файл audio_transcoder.go сжимает исходный аудиофайл (FLAC/MP3/M4A) на лету
// через системный бинарник ffmpeg в легкий моно-поток Ogg Opus (16 kHz, 32 kbps)
// напрямую в оперативную память без создания временных файлов на диске.
package ai

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"

	"github.com/F5GO/poisq/internal/security"
)

const maxTranscodedBytes = 15 * 1024 * 1024 // 15 МБ жесткий лимит буфера в памяти

// AudioTranscoder отвечает за быстрое in-memory сжатие аудио для отправки в Groq Whisper.
type AudioTranscoder struct {
	guard *security.PathGuard
}

// NewAudioTranscoder создает новый экземпляр транскодера аудио.
func NewAudioTranscoder(guard *security.PathGuard) *AudioTranscoder {
	return &AudioTranscoder{guard: guard}
}

// TranscodeToOpusMono16k сжимает аудиофайл в формат Ogg Opus (16 kHz, моно, 32 kbps) в памяти.
func (t *AudioTranscoder) TranscodeToOpusMono16k(ctx context.Context, audioFilePath string) ([]byte, error) {
	safePath, err := t.guard.ValidateTarget(audioFilePath, false)
	if err != nil {
		return nil, err
	}
	if !security.IsAudioFile(safePath) {
		return nil, fmt.Errorf("файл %s не является поддерживаемым аудиофайлом", safePath)
	}

	tCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	// Прямой вызов ffmpeg без shell (Zero Shell Execution) с выводом в stdout (pipe:1).
	// Фильтр highpass=f=100,lowpass=f=7500 отсекает саб-бас бочки (<100 Гц), который сбивает
	// детектор начала слов (DTW/VAD) в Whisper на музыкальных вступлениях, сохраняя всю полосу вокала.
	args := []string{
		"-hide_banner",
		"-loglevel", "error",
		"-i", safePath,
		"-vn",
		"-af", "highpass=f=100,lowpass=f=7500",
		"-ac", "1",
		"-ar", "16000",
		"-c:a", "libopus",
		"-b:a", "32k",
		"-f", "ogg",
		"pipe:1",
	}

	cmd := exec.CommandContext(tCtx, "ffmpeg", args...)
	var outBuf bytes.Buffer
	var errBuf bytes.Buffer
	cmd.Stdout = &limitedWriter{buf: &outBuf, max: maxTranscodedBytes}
	cmd.Stderr = &errBuf

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ошибка сжатия аудио через ffmpeg (%v): %s", err, errBuf.String())
	}

	if outBuf.Len() == 0 {
		return nil, fmt.Errorf("ffmpeg вернул пустой аудиопоток для %s", safePath)
	}

	return outBuf.Bytes(), nil
}

type limitedWriter struct {
	buf *bytes.Buffer
	max int
}

func (lw *limitedWriter) Write(p []byte) (int, error) {
	if lw.buf.Len()+len(p) > lw.max {
		return 0, fmt.Errorf("превышен лимит памяти для сжатого аудио (%d байт)", lw.max)
	}
	return lw.buf.Write(p)
}
