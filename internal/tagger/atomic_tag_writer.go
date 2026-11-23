// Package tagger реализует бережное чтение, хирургическую перезапись и откат (Undo)
// аудио-тегов по стандарту MusicBrainz Picard без перекодирования аудиопотока (Bit-Perfect).
//
// Данный файл atomic_tag_writer.go выполняет атомарную перезапись метаданных через
// временный файл (.poisq_tmp_*) с копированием потоков 1:1 (-c copy -map_metadata 0),
// сверкой контрольной суммы STREAMINFO MD5 для FLAC и контролем целостности перед os.Rename.
package tagger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/F5GO/poisq/internal/security"
)

// AtomicTagWriter выполняет безопасное обновление аудио-тегов на файловой системе.
type AtomicTagWriter struct {
	guard             *security.PathGuard
	ffmpegExecutable  string
	ffprobeExecutable string
}

// NewAtomicTagWriter создает новый экземпляр атомарного теггера.
func NewAtomicTagWriter(guard *security.PathGuard) *AtomicTagWriter {
	return &AtomicTagWriter{guard: guard, ffmpegExecutable: "ffmpeg", ffprobeExecutable: "ffprobe"}
}

type BackupInfo struct {
	TargetPath     string
	BackupPath     string
	OriginalExists bool
	OriginalSize   int64
	OriginalSHA256 string
}

func (w *AtomicTagWriter) PrepareBackup(targetPath, transactionID string) (BackupInfo, error) {
	if w == nil || w.guard == nil || !validTransactionID(transactionID) {
		return BackupInfo{}, fmt.Errorf("invalid backup request")
	}
	safeTarget, err := w.guard.ValidateTarget(targetPath, true)
	if err != nil {
		return BackupInfo{}, err
	}
	info := BackupInfo{TargetPath: safeTarget}
	stat, err := os.Stat(safeTarget)
	if os.IsNotExist(err) {
		return info, nil
	}
	if err != nil {
		return BackupInfo{}, fmt.Errorf("stat backup target: %w", err)
	}
	if !stat.Mode().IsRegular() {
		return BackupInfo{}, fmt.Errorf("backup target is not a regular file")
	}
	backupPath := filepath.Join(filepath.Dir(safeTarget), ".poisq_backup_"+transactionID+strings.ToLower(filepath.Ext(safeTarget)))
	source, err := os.Open(safeTarget)
	if err != nil {
		return BackupInfo{}, err
	}
	defer source.Close()
	backup, err := os.OpenFile(backupPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return BackupInfo{}, fmt.Errorf("create writeback backup: %w", err)
	}
	removeBackup := true
	defer func() {
		_ = backup.Close()
		if removeBackup {
			_ = os.Remove(backupPath)
		}
	}()
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(backup, hash), source)
	if err != nil || written != stat.Size() {
		return BackupInfo{}, fmt.Errorf("copy writeback backup: %w", err)
	}
	if err := backup.Sync(); err != nil {
		return BackupInfo{}, fmt.Errorf("sync writeback backup: %w", err)
	}
	if err := backup.Chmod(stat.Mode()); err != nil {
		return BackupInfo{}, fmt.Errorf("chmod writeback backup: %w", err)
	}
	if err := backup.Close(); err != nil {
		return BackupInfo{}, fmt.Errorf("close writeback backup: %w", err)
	}
	removeBackup = false
	info.BackupPath = backupPath
	info.OriginalExists = true
	info.OriginalSize = stat.Size()
	info.OriginalSHA256 = hex.EncodeToString(hash.Sum(nil))
	return info, nil
}

func (w *AtomicTagWriter) RestoreBackup(info BackupInfo, transactionID string) error {
	if w == nil || w.guard == nil || !validTransactionID(transactionID) {
		return fmt.Errorf("invalid restore request")
	}
	safeTarget, err := w.guard.ValidateTarget(info.TargetPath, true)
	if err != nil {
		return err
	}
	if !info.OriginalExists {
		if err := os.Remove(safeTarget); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove newly created target: %w", err)
		}
		return nil
	}
	expectedBackup := filepath.Join(filepath.Dir(safeTarget), ".poisq_backup_"+transactionID+strings.ToLower(filepath.Ext(safeTarget)))
	if info.BackupPath != expectedBackup || len(info.OriginalSHA256) != 64 {
		return fmt.Errorf("backup path or checksum mismatch")
	}
	backupStat, err := os.Lstat(expectedBackup)
	if err != nil || !backupStat.Mode().IsRegular() || backupStat.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("writeback backup unavailable")
	}
	hash, size, err := hashFile(expectedBackup)
	if err != nil || hash != info.OriginalSHA256 || size != info.OriginalSize {
		return fmt.Errorf("writeback backup integrity mismatch")
	}
	source, err := os.Open(expectedBackup)
	if err != nil {
		return err
	}
	defer source.Close()
	tmp, err := os.CreateTemp(filepath.Dir(safeTarget), ".poisq_restore_*"+filepath.Ext(safeTarget))
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	removeTemp := true
	defer func() {
		_ = tmp.Close()
		if removeTemp {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := io.Copy(tmp, source); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Chmod(backupStat.Mode()); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, safeTarget); err != nil {
		return fmt.Errorf("restore original file: %w", err)
	}
	removeTemp = false
	restoredHash, restoredSize, err := hashFile(safeTarget)
	if err != nil || restoredHash != info.OriginalSHA256 || restoredSize != info.OriginalSize {
		return fmt.Errorf("restored file integrity mismatch")
	}
	return nil
}

func validTransactionID(value string) bool {
	if len(value) != 32 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func hashFile(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

func (w *AtomicTagWriter) WriteFileAtomically(targetPath string, content []byte, maxBytes int) error {
	if maxBytes < 1 || len(content) > maxBytes {
		return fmt.Errorf("file content exceeds write limit")
	}
	safeTarget, err := w.guard.ValidateTarget(targetPath, true)
	if err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if stat, err := os.Stat(safeTarget); err == nil {
		if !stat.Mode().IsRegular() {
			return fmt.Errorf("write target is not a regular file")
		}
		mode = stat.Mode()
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(safeTarget), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(safeTarget), ".poisq_tmp_*"+filepath.Ext(safeTarget))
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	remove := true
	defer func() {
		_ = tmp.Close()
		if remove {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(content); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, safeTarget); err != nil {
		return err
	}
	remove = false
	return nil
}

func (w *AtomicTagWriter) VerifyFileContent(targetPath string, content []byte, maxBytes int) error {
	if maxBytes < 1 || len(content) > maxBytes {
		return fmt.Errorf("file content exceeds verification limit")
	}
	safeTarget, err := w.guard.ValidateTarget(targetPath, false)
	if err != nil {
		return err
	}
	file, err := os.Open(safeTarget)
	if err != nil {
		return err
	}
	defer file.Close()
	actual, err := io.ReadAll(io.LimitReader(file, int64(maxBytes)+1))
	if err != nil {
		return err
	}
	if len(actual) > maxBytes || !bytesEqual(actual, content) {
		return fmt.Errorf("post-write file verification mismatch")
	}
	return nil
}

func bytesEqual(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var difference byte
	for index := range left {
		difference |= left[index] ^ right[index]
	}
	return difference == 0
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
	tmpFile, err := os.CreateTemp(dir, ".poisq_tmp_*"+ext)
	if err != nil {
		return fmt.Errorf("не удалось создать временный файл для записи тегов: %w", err)
	}
	tmpPath := tmpFile.Name()
	_ = tmpFile.Close()

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

	cmd := exec.CommandContext(tCtx, w.ffmpegExecutable, args...)
	errBuf := &tagLimitedBuffer{limit: tagVerifyErrorMax}
	cmd.Stderr = errBuf

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
