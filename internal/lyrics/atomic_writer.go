// Package lyrics предоставляет систему поиска, ранжирования и сохранения текстов песен (Lyrics)
// по 5 мировым и региональным источникам (LRCLIB, Яндекс Музыка, Musixmatch, NetEase, Genius).
//
// Данный файл atomic_writer.go реализует безопасную атомарную запись файла .lrc
// рядом с исходным аудиофайлом в папке /music (через временный файл .poisq_tmp_*.lrc и os.Rename),
// чтобы музыкальный сервер Navidrome автоматически подхватывал синхронизированный текст.
package lyrics

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/F5GO/poisq/internal/security"
)

// AtomicLRCWriter безопасно записывает .lrc файлы рядом с аудиофайлами медиатеки.
type AtomicLRCWriter struct {
	guard *security.PathGuard
}

// NewAtomicLRCWriter создает экземпляр атомарного писателя .lrc файлов.
func NewAtomicLRCWriter(guard *security.PathGuard) *AtomicLRCWriter {
	return &AtomicLRCWriter{guard: guard}
}

// WriteForAudioFile вычисляет путь <audio_filename>.lrc рядом с аудиофайлом,
// проверяет путь через PathGuard и атомарно записывает содержимое.
func (w *AtomicLRCWriter) WriteForAudioFile(audioFilePath string, lrcContent string) (string, error) {
	content := strings.TrimSpace(lrcContent)
	if content == "" {
		return "", fmt.Errorf("пустое содержимое текста не может быть записано в .lrc")
	}

	ext := filepath.Ext(audioFilePath)
	targetLrc := strings.TrimSuffix(audioFilePath, ext) + ".lrc"

	safeTarget, err := w.guard.ValidateTarget(targetLrc, true)
	if err != nil {
		return "", err
	}

	dir := filepath.Dir(safeTarget)
	base := filepath.Base(safeTarget)
	tmpPath := filepath.Join(dir, ".poisq_tmp_"+base)

	// Записываем с завершающим переводом строки в UTF-8
	payload := []byte(content + "\n")
	if err := os.WriteFile(tmpPath, payload, 0644); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("ошибка записи временного .lrc файла: %w", err)
	}

	if err := os.Rename(tmpPath, safeTarget); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("ошибка атомарного переименования .lrc файла: %w", err)
	}

	return safeTarget, nil
}
