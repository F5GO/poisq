// Package security отвечает за криптографическую защиту API-ключей, проверку путей
// файловой системы медиатеки (/music) и защиту административного контура (:4535).
//
// Данный файл path_guard.go реализует строгую изоляцию файловых операций внутри
// корневой директории /music (защита от Path Traversal, Symlink Escape и модификации
// неразрешенных типов файлов).
package security

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var (
	// ErrPathTraversal возвращается при попытке выхода за пределы корневой папки /music.
	ErrPathTraversal = errors.New("отказано в доступе: путь выходит за пределы корневой директории медиатеки")

	// ErrForbiddenExtension возвращается при попытке записи файла с неразрешенным расширением.
	ErrForbiddenExtension = errors.New("отказано в доступе: недопустимое расширение файла")
)

var allowedAudioExts = map[string]bool{
	".flac": true,
	".mp3":  true,
	".m4a":  true,
	".ogg":  true,
	".opus": true,
	".wav":  true,
	".wv":   true,
}

var allowedWriteExts = map[string]bool{
	".flac": true,
	".mp3":  true,
	".m4a":  true,
	".ogg":  true,
	".opus": true,
	".lrc":  true,
	".jpg":  true,
	".jpeg": true,
	".png":  true,
}

// PathGuard проверяет и нормализует пути внутри примонтированной медиатеки.
type PathGuard struct {
	rootAbs string
}

// NewPathGuard создает экземпляр защитника путей для указанного корня медиатеки.
func NewPathGuard(musicRoot string) (*PathGuard, error) {
	cleaned := filepath.Clean(strings.TrimSpace(musicRoot))
	if cleaned == "" {
		return nil, errors.New("путь к медиатеке не может быть пустым")
	}
	absRoot, err := filepath.Abs(cleaned)
	if err != nil {
		return nil, fmt.Errorf("не удалось определить абсолютный путь медиатеки: %w", err)
	}
	// Если директория уже существует, разрешаем симлинки корня (актуально для macOS /var -> /private/var)
	if resolved, err := filepath.EvalSymlinks(absRoot); err == nil {
		absRoot = resolved
	}
	return &PathGuard{rootAbs: absRoot}, nil
}

// Root возвращает канонический абсолютный путь корня медиатеки.
func (g *PathGuard) Root() string {
	return g.rootAbs
}

// ValidateTarget проверяет, что целевой путь находится строго внутри корня медиатеки
// и не использует символические ссылки для выхода наружу (Symlink Escape).
func (g *PathGuard) ValidateTarget(targetPath string, checkWriteExt bool) (string, error) {
	if strings.TrimSpace(targetPath) == "" {
		return "", ErrPathTraversal
	}

	absTarget, err := filepath.Abs(filepath.Clean(targetPath))
	if err != nil {
		return "", ErrPathTraversal
	}

	// Разрешаем симлинки по ближайшему существующему родительскому каталогу
	absTarget = resolveExistingAncestor(absTarget)

	// Если сам целевой файл существует, проверяем, не является ли он симлинком наружу
	if info, err := os.Lstat(absTarget); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			resolvedFile, err := filepath.EvalSymlinks(absTarget)
			if err != nil {
				return "", ErrPathTraversal
			}
			absTarget = resolvedFile
		}
	}

	if !isSubpath(g.rootAbs, absTarget) {
		return "", fmt.Errorf("%w: %s", ErrPathTraversal, targetPath)
	}

	baseName := filepath.Base(absTarget)
	if strings.HasPrefix(baseName, ".") && !strings.HasPrefix(baseName, ".poisq_tmp_") {
		return "", fmt.Errorf("%w: запрещена модификация скрытых системных файлов (%s)", ErrForbiddenExtension, baseName)
	}

	if checkWriteExt {
		ext := strings.ToLower(filepath.Ext(absTarget))
		if !allowedWriteExts[ext] {
			return "", fmt.Errorf("%w: %s", ErrForbiddenExtension, ext)
		}
	}

	return absTarget, nil
}

// resolveExistingAncestor находит ближайший существующий родительский каталог,
// разрешает для него символические ссылки (например, /var -> /private/var на macOS)
// и достраивает оставшийся относительный хвост пути.
func resolveExistingAncestor(absPath string) string {
	cur := absPath
	var tail []string
	for cur != "/" && cur != "." {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, tail[i])
			}
			return resolved
		}
		tail = append(tail, filepath.Base(cur))
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	return absPath
}

// IsAudioFile проверяет, является ли файл поддерживаемым аудиоформатом.
func IsAudioFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return allowedAudioExts[ext]
}

func isSubpath(root, target string) bool {
	if target == root {
		return true
	}
	prefix := root
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	return strings.HasPrefix(target, prefix)
}
