// Package metadata отвечает за аудит медиатеки, исправление кодировки, идентификацию и обложки.
//
// Данный файл image_reader_test.go тестирует модуль быстрого и отказоустойчивого парсинга
// размеров графических файлов (JPEG, PNG, WebP).
package metadata

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestDetectImageDimensionsPNG(t *testing.T) {
	tmpDir := t.TempDir()
	pngPath := filepath.Join(tmpDir, "test.png")

	img := image.NewRGBA(image.Rect(0, 0, 800, 600))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})

	f, err := os.Create(pngPath)
	if err != nil {
		t.Fatalf("os.Create error: %v", err)
	}
	if err := png.Encode(f, img); err != nil {
		_ = f.Close()
		t.Fatalf("png.Encode error: %v", err)
	}
	_ = f.Close()

	meta := DetectImageDimensions(pngPath)
	if !meta.Valid {
		t.Fatalf("expected valid image, got invalid")
	}
	if meta.Width != 800 || meta.Height != 600 {
		t.Fatalf("expected 800x600, got %dx%d", meta.Width, meta.Height)
	}
	if meta.Format != "png" {
		t.Fatalf("expected png format, got %s", meta.Format)
	}
}
