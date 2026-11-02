// Package tagger реализует бережное чтение, хирургическую перезапись и откат (Undo)
// аудио-тегов по стандарту MusicBrainz Picard без перекодирования аудиопотока (Bit-Perfect).
//
// Данный файл atomic_tag_writer.go выполняет атомарную перезапись метаданных через
// временный файл (.poisq_tmp_*) с копированием потоков 1:1 (-c copy -map_metadata 0),
// сверкой контрольной суммы STREAMINFO MD5 для FLAC и контролем целостности перед os.Rename.
package tagger

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/F5GO/poisq/internal/security"
)

// AtomicTagWriter выполняет безопасное обновление аудио-тегов на файловой системе.
type AtomicTagWriter struct {
	guard *security.PathGuard
}

// NewAtomicTagWriter создает новый экземпляр атомарного теггера.
func NewAtomicTagWriter(guard *security.PathGuard) *AtomicTagWriter {
	return &AtomicTagWriter{guard: guard}
}

// WriteTagsAtomically записывает новые метаданные в копию файла без перекодирования звука,
// проверяет сохранность аудиопотока (включая STREAMINFO MD5 для FLAC) и атомарно заменяет оригинал.
func (w *AtomicTagWriter) WriteTagsAtomically(ctx context.Context, audioFilePath string, tags CanonicalTagSet) error {
	safeTarget, err := w.guard.ValidateTarget(audioFilePath, true)
	if err != nil {
		return err
	}
	if !security.IsAudioFile(safeTarget) {
		return fmt.Errorf("файл %s не является поддерживаемым аудиоформатом", safeTarget)
	}

	origStat, err := os.Stat(safeTarget)
	if err != nil {
		return fmt.Errorf("исходный аудиофайл недоступен: %w", err)
	}

	ext := strings.ToLower(filepath.Ext(safeTarget))
	var origFlacMD5 string
	if ext == ".flac" {
		origFlacMD5, _ = ExtractFLACAudioMD5(safeTarget)
	}

	dir := filepath.Dir(safeTarget)
	base := filepath.Base(safeTarget)
	tmpPath := filepath.Join(dir, ".poisq_tmp_"+base)
	_ = os.Remove(tmpPath)

	tCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Формируем команду ffmpeg: копируем все потоки 1:1 без перекодирования (-map 0 -c copy)
	// и переносим все существующие пользовательские теги (-map_metadata 0), поверх которых
	// прописываем обновленные каноничные теги по таблице MusicBrainz Picard.
	args := []string{
		"-hide_banner",
		"-loglevel", "error",
		"-y",
		"-i", safeTarget,
		"-map", "0",
		"-c", "copy",
		"-map_metadata", "0",
	}

	if ext == ".mp3" {
		args = append(args, "-id3v2_version", "4", "-write_id3v1", "0")
	}

	args = append(args, BuildFFmpegMetadataArgs(safeTarget, tags)...)
	args = append(args, tmpPath)

	cmd := exec.CommandContext(tCtx, "ffmpeg", args...)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf

	if err := cmd.Run(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("ошибка записи тегов через ffmpeg (%v): %s", err, errBuf.String())
	}

	// Проверка 1: Размер нового файла должен быть валидным (не менее 95% от исходного)
	newStat, err := os.Stat(tmpPath)
	if err != nil || newStat.Size() == 0 || float64(newStat.Size()) < float64(origStat.Size())*0.95 {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("проверка целостности не пройдена: размер временного файла подозрительно мал")
	}

	// Проверка 2 (FLAC Bit-Perfect Check): Сравниваем 16-байтную сигнатуру STREAMINFO MD5
	if ext == ".flac" && origFlacMD5 != "" {
		newFlacMD5, err := ExtractFLACAudioMD5(tmpPath)
		if err != nil || newFlacMD5 != origFlacMD5 {
			_ = os.Remove(tmpPath)
			return fmt.Errorf("проверка Bit-Perfect не пройдена: контрольная сумма FLAC STREAMINFO изменилась (%s != %s)", origFlacMD5, newFlacMD5)
		}
	}

	// Восстанавливаем права доступа исходного файла
	_ = os.Chmod(tmpPath, origStat.Mode())

	// Атомарная замена оригинала
	if err := os.Rename(tmpPath, safeTarget); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("ошибка атомарной замены аудиофайла: %w", err)
	}

	return nil
}
