// Package metadata отвечает за аудит медиатеки, исправление битой кодировки (CP1251 -> UTF-8 NFC),
// акустическую идентификацию через AcoustID (fpcalc), обогащение через MusicBrainz API и поиск обложек.
//
// Данный файл cover_locator.go реализует глубокий, регистронезависимый и всепрощающий поиск
// локальных файлов обложек альбомов и фотографий исполнителей (cover.jpg, folder.jpg, artist.jpg, front.png, webp).
// Устойчив к различиям в тире/дефисах (- / – / —), нормализации Unicode (NFC/NFD на macOS APFS),
// вариациям путей Navidrome (/music vs /music/Music vs «Music — копия»), дочерним папкам альбомов,
// родительским каталогам и подпапкам со сканами, с подробным отладочным логированием (LOG_LEVEL=debug).
package metadata

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/F5GO/poisq/internal/logger"
)

var priorityCoverBases = []string{
	"cover",
	"folder",
	"front",
	"album",
	"art",
	"artist",
	"default",
	"thumb",
}

var supportedImageExts = map[string]bool{
	".jpg":  true,
	".jpeg": true,
	".png":  true,
	".webp": true,
}

var artworkSubdirs = []string{
	"artwork",
	"scans",
	"scan",
	"covers",
	"images",
	"art",
}

// FindLocalCoverFile сканирует директорию folderPath и возвращает путь к найденной обложке.
// Сохраняет обратную совместимость с вызовами по всей кодовой базе.
func FindLocalCoverFile(folderPath string) string {
	return FindCoverAnywhere("", folderPath)
}

// FindCoverAnywhere выполняет всесторонний поиск файла обложки на файловой системе:
// 1. Генерирует все вариации пути (включая устранение « — копия», префиксы Music/, разные корни).
// 2. Сканирует целевой каталог на приоритетные имена (cover, folder, front, artist, etc.).
// 3. Проверяет вложенные подпапки со сканами (Artwork/, Scans/) и дочерние папки альбомов.
// 4. Поднимается вверх по родительским папкам альбома/артиста.
// 5. Выполняет нечеткий поиск (Fuzzy Match) при различиях в тире, пробелах, регистре и суффиксах копий.
func FindCoverAnywhere(musicRoot, targetPath string) string {
	clean := filepath.Clean(strings.TrimSpace(targetPath))
	if clean == "" || clean == "." || clean == "/" {
		return ""
	}

	// Если передано несколько файлов через разделитель (в Navidrome image_files: \x01, ;, | или \n)
	for _, sep := range []string{";", "|", "\n", "\x01"} {
		if strings.Contains(clean, sep) {
			for _, part := range strings.Split(clean, sep) {
				if p := strings.TrimSpace(part); p != "" {
					if found := FindCoverAnywhere(musicRoot, p); found != "" {
						return found
					}
				}
			}
			return ""
		}
	}

	candidatePaths := GeneratePathVariants(musicRoot, targetPath)
	if len(candidatePaths) == 0 {
		candidatePaths = []string{clean}
	}

	for _, cand := range candidatePaths {
		if found := findCoverInSingleTarget(musicRoot, cand); found != "" {
			logger.Debug("[COVER] Найдена обложка на диске: %s (запрос: %s, вариант: %s)", found, targetPath, cand)
			return found
		}
	}

	logger.Debug("[COVER] Обложка не найдена на диске для пути %q (проверено %d вариантов путей)", targetPath, len(candidatePaths))
	return ""
}

func findCoverInSingleTarget(musicRoot, clean string) string {
	// 1. Если передан путь к существующему файлу обложки
	if fi, err := os.Stat(clean); err == nil && !fi.IsDir() {
		ext := strings.ToLower(filepath.Ext(clean))
		if supportedImageExts[ext] && fi.Size() > 0 {
			return clean
		}
		// Если передан аудиофайл — исследуем каталог, в котором он лежит
		clean = filepath.Dir(clean)
	}

	// 2. Прямой поиск в директории и ее дочерних подпапках
	if found := scanDirectoryAndSubdirsForCover(clean); found != "" {
		return found
	}

	// 3. Поиск по родительским папкам (до 3 уровней вверх, не выходя за musicRoot)
	cur := clean
	for i := 0; i < 3; i++ {
		parent := filepath.Dir(cur)
		if parent == cur || parent == "." || parent == "/" {
			break
		}
		if musicRoot != "" && !strings.HasPrefix(parent, musicRoot) && parent != musicRoot {
			break
		}
		if found := scanDirectoryForCoverOnly(parent); found != "" {
			return found
		}
		cur = parent
	}

	// 4. Нечеткий поиск реального каталога на диске (Fuzzy Path Resolution)
	resolvedDir := ResolveFuzzyDirectory(musicRoot, clean)
	if resolvedDir != "" && resolvedDir != clean {
		if found := scanDirectoryAndSubdirsForCover(resolvedDir); found != "" {
			return found
		}
		// Проверяем родителей нечетко найденного каталога
		p := filepath.Dir(resolvedDir)
		if p != resolvedDir && p != "." && p != "/" {
			if found := scanDirectoryForCoverOnly(p); found != "" {
				return found
			}
		}
	}

	return ""
}

// FindCoverByArtistAndAlbum ищет обложку на диске напрямую по имени исполнителя и альбома,
// даже если сохраненный в БД путь отличался от реальной структуры папок.
func FindCoverByArtistAndAlbum(musicRoot, artist, album string) string {
	roots := []string{}
	if musicRoot != "" {
		roots = append(roots, musicRoot, filepath.Join(musicRoot, "Music"), filepath.Join(musicRoot, "music"))
	}
	roots = append(roots, "/music", "/music/Music")

	seen := make(map[string]bool)
	for _, r := range roots {
		r = filepath.Clean(r)
		if seen[r] {
			continue
		}
		seen[r] = true
		fi, err := os.Stat(r)
		if err != nil || !fi.IsDir() {
			continue
		}

		if artist != "" && album != "" {
			for _, p := range []string{
				filepath.Join(r, artist, album),
				filepath.Join(r, artist, artist+" - "+album),
				filepath.Join(r, artist, artist+" – "+album),
				filepath.Join(r, artist, artist+" — "+album),
			} {
				if resolved := ResolveFuzzyDirectory(r, p); resolved != "" {
					if found := scanDirectoryAndSubdirsForCover(resolved); found != "" {
						return found
					}
				}
			}
		}

		if artist != "" {
			if resolved := ResolveFuzzyDirectory(r, filepath.Join(r, artist)); resolved != "" {
				if found := scanDirectoryAndSubdirsForCover(resolved); found != "" {
					return found
				}
			}
		}
	}
	return ""
}

// scanDirectoryAndSubdirsForCover ищет обложку в каталоге, его подпапках Artwork/Scans
// и дочерних папках первого уровня.
func scanDirectoryAndSubdirsForCover(dirPath string) string {
	// Сначала ищем в самой директории
	if found := scanDirectoryForCoverOnly(dirPath); found != "" {
		return found
	}

	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return ""
	}

	// 1-й проход: проверяем типовые подкаталоги со сканами (Artwork, Scans, Covers)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		nameLower := strings.ToLower(e.Name())
		for _, sub := range artworkSubdirs {
			if nameLower == sub {
				subDir := filepath.Join(dirPath, e.Name())
				if found := scanDirectoryForCoverOnly(subDir); found != "" {
					return found
				}
				break
			}
		}
	}

	// 2-й проход: проверяем дочерние подкаталоги первого уровня (до 15 подпапок альбомов/дисков)
	checkedSubdirs := 0
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		subDir := filepath.Join(dirPath, e.Name())
		if found := scanDirectoryForCoverOnly(subDir); found != "" {
			return found
		}
		checkedSubdirs++
		if checkedSubdirs >= 15 {
			break
		}
	}

	return ""
}

// scanDirectoryForCoverOnly сканирует одну физическую директорию на наличие изображений.
func scanDirectoryForCoverOnly(dirPath string) string {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return ""
	}

	// 1-й проход: ищем файлы с приоритетными базовыми именами (cover, folder, front, album, art, artist)
	for _, baseName := range priorityCoverBases {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			nameLower := strings.ToLower(entry.Name())
			ext := filepath.Ext(nameLower)
			if !supportedImageExts[ext] {
				continue
			}
			base := strings.TrimSuffix(nameLower, ext)
			if base == baseName {
				full := filepath.Join(dirPath, entry.Name())
				if fi, err := entry.Info(); err == nil && fi.Size() > 0 {
					return full
				}
			}
		}
	}

	// 2-й проход: ищем любые другие графические файлы в папке
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		nameLower := strings.ToLower(entry.Name())
		ext := filepath.Ext(nameLower)
		if supportedImageExts[ext] {
			full := filepath.Join(dirPath, entry.Name())
			if fi, err := entry.Info(); err == nil && fi.Size() > 0 {
				return full
			}
		}
	}

	return ""
}

// ResolveFuzzyDirectory находит реальный существующий каталог на диске,
// разрешая расхождения в тире (- vs – vs —), суффиксах копий (« — копия»),
// Unicode NFC/NFD, пробелах, регистре и вариациях префикса библиотеки.
func ResolveFuzzyDirectory(musicRoot, targetPath string) string {
	clean := filepath.Clean(strings.TrimSpace(targetPath))
	if clean == "" || clean == "." || clean == "/" {
		return ""
	}

	// Проверяем, существует ли папка прямо сейчас
	if fi, err := os.Stat(clean); err == nil && fi.IsDir() {
		return clean
	}

	// Определяем отправную точку поиска
	startRoot := "/"
	if musicRoot != "" {
		if fi, err := os.Stat(musicRoot); err == nil && fi.IsDir() {
			startRoot = musicRoot
		}
	}

	// Отрезаем начальный корень
	relPath := clean
	if strings.HasPrefix(clean, startRoot) {
		relPath = strings.TrimPrefix(clean, startRoot)
	} else if strings.HasPrefix(clean, "/music") {
		relPath = strings.TrimPrefix(clean, "/music")
	}
	relPath = strings.TrimPrefix(relPath, "/")

	segments := strings.Split(relPath, string(filepath.Separator))
	if len(segments) == 0 {
		return ""
	}

	// Пробуем обойти дерево от startRoot
	current := startRoot
	for i, seg := range segments {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}

		direct := filepath.Join(current, seg)
		if fi, err := os.Stat(direct); err == nil && fi.IsDir() {
			current = direct
			continue
		}

		// Ищем совпадение среди подкаталогов с нормализацией
		entries, err := os.ReadDir(current)
		if err != nil {
			break
		}

		targetNorm := normalizeNameForMatch(seg)
		targetCleanNorm := stripCopySuffix(targetNorm)
		foundMatch := false

		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			eNorm := normalizeNameForMatch(e.Name())
			eCleanNorm := stripCopySuffix(eNorm)

			if eNorm == targetNorm || eCleanNorm == targetCleanNorm || (targetCleanNorm != "" && eNorm == targetCleanNorm) {
				current = filepath.Join(current, e.Name())
				foundMatch = true
				break
			}
		}

		// Если совпадение не найдено, проверяем случай, когда сегмент 'Music'/'music' отсутствует на диске
		if !foundMatch && (strings.EqualFold(seg, "music") || strings.EqualFold(seg, "audio") || strings.EqualFold(targetCleanNorm, "music")) {
			continue
		}

		// Сегмент 'Music'/'music' может присутствовать на диске, но отсутствовать в запросе
		if !foundMatch {
			for _, e := range entries {
				if e.IsDir() && (strings.EqualFold(e.Name(), "music") || strings.EqualFold(e.Name(), "audio")) {
					musicSub := filepath.Join(current, e.Name())
					if subEntries, err := os.ReadDir(musicSub); err == nil {
						for _, se := range subEntries {
							if se.IsDir() {
								seNorm := normalizeNameForMatch(se.Name())
								if seNorm == targetNorm || stripCopySuffix(seNorm) == targetCleanNorm {
									current = filepath.Join(musicSub, se.Name())
									foundMatch = true
									break
								}
							}
						}
					}
				}
				if foundMatch {
					break
				}
			}
		}

		// Если это имя папки альбома с префиксом исполнителя (например, 'Король и Шут - Будь как дома')
		if !foundMatch && strings.Contains(targetNorm, "-") {
			parts := strings.Split(targetNorm, "-")
			afterDash := strings.TrimSpace(parts[len(parts)-1])
			if len(afterDash) > 3 {
				for _, e := range entries {
					if !e.IsDir() {
						continue
					}
					eNorm := normalizeNameForMatch(e.Name())
					if strings.Contains(eNorm, afterDash) || strings.Contains(afterDash, eNorm) {
						current = filepath.Join(current, e.Name())
						foundMatch = true
						break
					}
				}
			}
		}

		if !foundMatch && i == len(segments)-1 {
			return current
		}
	}

	if fi, err := os.Stat(current); err == nil && fi.IsDir() {
		return current
	}

	return ""
}

func stripCopySuffix(s string) string {
	return normalizeNameForMatch(StripCopySuffix(s))
}

func normalizeNameForMatch(s string) string {
	res := strings.ToLower(strings.TrimSpace(NormalizeNFC(NormalizePathSpaces(s))))
	res = strings.ReplaceAll(res, "–", "-")
	res = strings.ReplaceAll(res, "—", "-")
	res = strings.ReplaceAll(res, "_", "-")
	for strings.Contains(res, "  ") {
		res = strings.ReplaceAll(res, "  ", " ")
	}
	return res
}
