// Package metadata отвечает за аудит медиатеки, исправление битой кодировки (CP1251 -> UTF-8 NFC),
// обогащение через MusicBrainz API и поиск обложек.
//
// Данный файл image_reader.go реализует быстрый и отказоустойчивый парсер размеров графических файлов
// (JPEG, PNG, WebP) без внешних зависимостей. Гарантирует правильное определение размеров даже для
// прогрессивных JPEG, WebP и файлов с нестандартными EXIF-заголовками.
package metadata

import (
	"bytes"
	"encoding/binary"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
)

// ImageMeta содержит размеры и формат графического файла обложки.
type ImageMeta struct {
	Width  int
	Height int
	Format string
	Valid  bool
}

// DetectImageDimensions считывает размеры изображения из файла.
// Сначала пробует стандартный image.DecodeConfig, а при неудаче выполняет
// прямой побайтовый парсинг заголовков JPEG, PNG и WebP.
func DetectImageDimensions(filePath string) ImageMeta {
	f, err := os.Open(filePath)
	if err != nil {
		return ImageMeta{}
	}
	defer f.Close()

	// 1. Стандартный Go image.DecodeConfig
	if cfg, fmtName, err := image.DecodeConfig(f); err == nil && cfg.Width > 0 && cfg.Height > 0 {
		return ImageMeta{
			Width:  cfg.Width,
			Height: cfg.Height,
			Format: fmtName,
			Valid:  true,
		}
	}

	// 2. Считываем первые 64 КБ для прямого анализа сигнатур
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return ImageMeta{}
	}

	header := make([]byte, 64*1024)
	n, err := io.ReadFull(f, header)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return ImageMeta{}
	}
	buf := header[:n]

	// 2a. Проверка WebP (RIFF....WEBP)
	if len(buf) >= 30 && bytes.Equal(buf[0:4], []byte("RIFF")) && bytes.Equal(buf[8:12], []byte("WEBP")) {
		w, h, ok := parseWebPDimensions(buf)
		if ok {
			return ImageMeta{Width: w, Height: h, Format: "webp", Valid: true}
		}
		return ImageMeta{Format: "webp", Valid: true}
	}

	// 2b. Проверка PNG (\x89PNG\r\n\x1a\n)
	if len(buf) >= 24 && bytes.Equal(buf[0:8], []byte("\x89PNG\r\n\x1a\n")) {
		w := int(binary.BigEndian.Uint32(buf[16:20]))
		h := int(binary.BigEndian.Uint32(buf[20:24]))
		if w > 0 && h > 0 {
			return ImageMeta{Width: w, Height: h, Format: "png", Valid: true}
		}
		return ImageMeta{Format: "png", Valid: true}
	}

	// 2c. Проверка JPEG (\xFF\xD8\xFF)
	if len(buf) >= 4 && buf[0] == 0xFF && buf[1] == 0xD8 && buf[2] == 0xFF {
		w, h, ok := parseJPEGDimensions(buf)
		if ok {
			return ImageMeta{Width: w, Height: h, Format: "jpeg", Valid: true}
		}
		return ImageMeta{Format: "jpeg", Valid: true}
	}

	return ImageMeta{}
}

func parseWebPDimensions(buf []byte) (int, int, bool) {
	if len(buf) < 16 {
		return 0, 0, false
	}
	chunkType := string(buf[12:16])
	switch chunkType {
	case "VP8 ": // Lossy
		if len(buf) >= 30 && buf[23] == 0x9D && buf[24] == 0x01 && buf[25] == 0x2A {
			w := int(binary.LittleEndian.Uint16(buf[26:28])) & 0x3FFF
			h := int(binary.LittleEndian.Uint16(buf[28:30])) & 0x3FFF
			if w > 0 && h > 0 {
				return w, h, true
			}
		}
	case "VP8L": // Lossless
		if len(buf) >= 25 && buf[20] == 0x2F {
			b0 := uint32(buf[21])
			b1 := uint32(buf[22])
			b2 := uint32(buf[23])
			b3 := uint32(buf[24])
			val := b0 | (b1 << 8) | (b2 << 16) | (b3 << 24)
			w := int((val & 0x3FFF) + 1)
			h := int(((val >> 14) & 0x3FFF) + 1)
			if w > 0 && h > 0 {
				return w, h, true
			}
		}
	case "VP8X": // Extended
		if len(buf) >= 30 {
			w := int(buf[24]) | (int(buf[25]) << 8) | (int(buf[26]) << 16) + 1
			h := int(buf[27]) | (int(buf[28]) << 8) | (int(buf[29]) << 16) + 1
			if w > 0 && h > 0 {
				return w, h, true
			}
		}
	}
	return 0, 0, false
}

func parseJPEGDimensions(buf []byte) (int, int, bool) {
	idx := 2
	for idx < len(buf)-8 {
		if buf[idx] != 0xFF {
			idx++
			continue
		}
		marker := buf[idx+1]
		// Игнорируем маркеры заполнения и префиксы
		if marker == 0xFF || marker == 0x00 {
			idx += 2
			continue
		}
		// Длина сегмента
		if idx+4 > len(buf) {
			break
		}
		segLen := int(binary.BigEndian.Uint16(buf[idx+2 : idx+4]))
		if segLen < 2 {
			break
		}

		// SOF0, SOF1, SOF2 (маркеры начала фрейма с размерами)
		if (marker >= 0xC0 && marker <= 0xC3) || (marker >= 0xC5 && marker <= 0xC7) || (marker >= 0xC9 && marker <= 0xCB) || (marker >= 0xCD && marker <= 0xCF) {
			if idx+9 <= len(buf) {
				h := int(binary.BigEndian.Uint16(buf[idx+5 : idx+7]))
				w := int(binary.BigEndian.Uint16(buf[idx+7 : idx+9]))
				if w > 0 && h > 0 {
					return w, h, true
				}
			}
		}

		// SOS (Start of Scan) — дальше начинаются данные сжатия
		if marker == 0xDA {
			break
		}

		idx += 2 + segLen
	}
	return 0, 0, false
}
