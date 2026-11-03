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

// PathResolverFunc определяет сигнатуру функции сопоставления пути к реальному файлу на диске.
type PathResolverFunc func(musicRoot, targetPath string) string

// AtomicLRCWriter безопасно записывает .lrc файлы рядом с аудиофайлами медиатеки.
type AtomicLRCWriter struct {
	guard    *security.PathGuard
	resolver PathResolverFunc
}

// NewAtomicLRCWriter создает экземпляр атомарного писателя .lrc файлов.
func NewAtomicLRCWriter(guard *security.PathGuard) *AtomicLRCWriter {
	return &AtomicLRCWriter{guard: guard}
}

// SetResolver подключает функцию поиска реального аудиофайла на диске при расхождениях путей.
func (w *AtomicLRCWriter) SetResolver(resolver PathResolverFunc) {
	w.resolver = resolver
}

// WriteForAudioFile вычисляет путь <audio_filename>.lrc рядом с аудиофайлом,
// разрешает путь к физическому файлу на диске при расхождениях между Navidrome и файловой системой,
// проверяет путь через PathGuard и атомарно записывает содержимое.
func (w *AtomicLRCWriter) WriteForAudioFile(audioFilePath string, lrcContent string) (string, error) {
	content := strings.TrimSpace(lrcContent)
	if content == "" {
		return "", fmt.Errorf("пустое содержимое текста не может быть записано в .lrc")
	}

	root := ""
	if w.guard != nil {
		root = w.guard.Root()
	}

	realAudioPath := audioFilePath
	if w.resolver != nil {
		if found := w.resolver(root, audioFilePath); found != "" {
			realAudioPath = found
		}
	}

	ext := filepath.Ext(realAudioPath)
	targetLrc := strings.TrimSuffix(realAudioPath, ext) + ".lrc"

	safeTarget, err := w.guard.ValidateTarget(targetLrc, true)
	if err != nil {
		return "", err
	}

	dir := filepath.Dir(safeTarget)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("ошибка создания директории для .lrc: %w", err)
	}

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
