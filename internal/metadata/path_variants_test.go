// Package metadata отвечает за аудит медиатеки, исправление битой кодировки (CP1251 -> UTF-8 NFC),
// обогащение через MusicBrainz API и поиск обложек.
//
// Данный файл path_variants_test.go проверяет генерацию альтернативных путей
// с устранением дубликатов ("Music — копия") и префиксов.
package metadata

import (
	"slices"
	"testing"
)

func TestGeneratePathVariants(t *testing.T) {
	musicRoot := "/music"
	targetPath := "/music/Music\u00a0— копия/Король и Шут/Король и Шут – Будь как дома, Путник"

	variants := GeneratePathVariants(musicRoot, targetPath)

	// Ожидаем наличие исходного пути
	if !slices.Contains(variants, targetPath) {
		t.Errorf("expected variants to contain targetPath %s", targetPath)
	}

	// Ожидаем вариант без « — копия»
	expectedClean := "/music/Music/Король и Шут/Король и Шут – Будь как дома, Путник"
	if !slices.Contains(variants, expectedClean) {
		t.Errorf("expected variants to contain %s, got: %v", expectedClean, variants)
	}

	// Ожидаем вариант без Music префикса
	expectedDirect := "/music/Король и Шут/Король и Шут – Будь как дома, Путник"
	if !slices.Contains(variants, expectedDirect) {
		t.Errorf("expected variants to contain %s, got: %v", expectedDirect, variants)
	}
}
