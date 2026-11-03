// Package metadata отвечает за аудит медиатеки, исправление битой кодировки (CP1251 -> UTF-8 NFC),
// акустическую идентификацию через AcoustID (fpcalc), обогащение через MusicBrainz API и поиск обложек.
//
// Данный файл embedded_cover.go реализует нативное извлечение встроенных обложек (Embedded Cover Art)
// непосредственно из аудиофайлов (ID3v2 APIC/PIC в MP3 и METADATA_BLOCK_PICTURE в FLAC)
// с резервным вызовом ffmpeg для M4A/MP4/OGG/Opus. Это гарантирует отображение обложек даже тогда,
// когда в папке альбома нет отдельного файла cover.jpg, но обложка зашита внутри аудиофайла.
package metadata

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ExtractCoverFromAudioOrDir проверяет путь (аудиофайл или директорию с аудиофайлами)
// и извлекает встроенную в теги картинку обложки (JPEG/PNG/WebP).
func ExtractCoverFromAudioOrDir(targetPath string) ([]byte, string) {
	clean := filepath.Clean(strings.TrimSpace(targetPath))
	if clean == "" || clean == "." || clean == "/" {
		return nil, ""
	}

	fi, err := os.Stat(clean)
	if err != nil {
		return nil, ""
	}

	if !fi.IsDir() {
		return ExtractCoverFromAudioFile(clean)
	}

	// Если передана директория — проверяем до 3 аудиофайлов внутри нее (или в ее подпапках первого уровня)
	entries, err := os.ReadDir(clean)
	if err != nil {
		return nil, ""
	}

	checked := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if isSupportedAudioExt(ext) {
			if data, mime := ExtractCoverFromAudioFile(filepath.Join(clean, entry.Name())); len(data) > 0 {
				return data, mime
			}
			checked++
			if checked >= 3 {
				break
			}
		}
	}

	// Если в самой папке аудиофайлов не было (например, это папка артиста), проверяем дочерние папки альбомов
	if checked == 0 {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			subDir := filepath.Join(clean, entry.Name())
			subEntries, err := os.ReadDir(subDir)
			if err != nil {
				continue
			}
			for _, se := range subEntries {
				if se.IsDir() {
					continue
				}
				if isSupportedAudioExt(strings.ToLower(filepath.Ext(se.Name()))) {
					if data, mime := ExtractCoverFromAudioFile(filepath.Join(subDir, se.Name())); len(data) > 0 {
						return data, mime
					}
					checked++
					if checked >= 3 {
						return nil, ""
					}
				}
			}
		}
	}

	return nil, ""
}

// ExtractCoverFromAudioFile извлекает встроенную обложку из конкретного аудиофайла.
func ExtractCoverFromAudioFile(audioPath string) ([]byte, string) {
	ext := strings.ToLower(filepath.Ext(audioPath))
	switch ext {
	case ".mp3":
		if data, mime := extractID3v2APIC(audioPath); len(data) > 0 {
			return data, mime
		}
	case ".flac":
		if data, mime := extractFLACPicture(audioPath); len(data) > 0 {
			return data, mime
		}
	}

	// Универсальный фоллбэк через ffmpeg (поддерживает m4a, mp4, ogg, opus, wv, mp3, flac)
	return extractCoverViaFFmpeg(audioPath)
}

// extractID3v2APIC читает заголовок ID3v2 (v2.2, v2.3, v2.4) и извлекает фрейм APIC/PIC без внешних библиотек.
func extractID3v2APIC(filePath string) ([]byte, string) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, ""
	}
	defer f.Close()

	var header [10]byte
	if _, err := io.ReadFull(f, header[:]); err != nil {
		return nil, ""
	}
	if string(header[0:3]) != "ID3" {
		return nil, ""
	}

	version := header[3]
	flags := header[5]
	tagSize := decodeSynchsafeInt(header[6:10])
	if tagSize <= 0 || tagSize > 30*1024*1024 {
		return nil, ""
	}

	tagBuf := make([]byte, tagSize)
	if _, err := io.ReadFull(f, tagBuf); err != nil {
		return nil, ""
	}

	pos := 0
	// Если установлен бит расширенного заголовка (Extended Header)
	if flags&0x40 != 0 && len(tagBuf) >= 4 {
		var extSize int
		if version == 4 {
			extSize = decodeSynchsafeInt(tagBuf[0:4])
		} else {
			extSize = int(binary.BigEndian.Uint32(tagBuf[0:4])) + 4
		}
		if extSize > 0 && extSize < len(tagBuf) {
			pos += extSize
		}
	}

	for pos < len(tagBuf) {
		if version == 2 {
			if pos+6 > len(tagBuf) {
				break
			}
			frameID := string(tagBuf[pos : pos+3])
			if frameID == "\x00\x00\x00" {
				break
			}
			frameSize := int(tagBuf[pos+3])<<16 | int(tagBuf[pos+4])<<8 | int(tagBuf[pos+5])
			pos += 6
			if frameSize <= 0 || pos+frameSize > len(tagBuf) {
				break
			}
			if frameID == "PIC" {
				if img, mime := parseImageSignature(tagBuf[pos : pos+frameSize]); len(img) > 0 {
					return img, mime
				}
			}
			pos += frameSize
		} else {
			if pos+10 > len(tagBuf) {
				break
			}
			frameID := string(tagBuf[pos : pos+4])
			if frameID == "\x00\x00\x00\x00" {
				break
			}
			var frameSize int
			if version == 4 {
				frameSize = decodeSynchsafeInt(tagBuf[pos+4 : pos+8])
			} else {
				frameSize = int(binary.BigEndian.Uint32(tagBuf[pos+4 : pos+8]))
			}
			pos += 10
			if frameSize <= 0 || pos+frameSize > len(tagBuf) {
				break
			}
			if frameID == "APIC" {
				if img, mime := parseAPICPayload(tagBuf[pos : pos+frameSize]); len(img) > 0 {
					return img, mime
				}
			}
			pos += frameSize
		}
	}

	return nil, ""
}

func parseAPICPayload(payload []byte) ([]byte, string) {
	if len(payload) < 16 {
		return nil, ""
	}
	// Надежно находим начало JPEG/PNG/WebP внутри фрейма APIC
	return parseImageSignature(payload)
}

// extractFLACPicture читает блоки метаданных FLAC и извлекает блок PICTURE (тип 6).
func extractFLACPicture(filePath string) ([]byte, string) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, ""
	}
	defer f.Close()

	var magic [4]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil || string(magic[:]) != "fLaC" {
		return nil, ""
	}

	for {
		var blockHeader [4]byte
		if _, err := io.ReadFull(f, blockHeader[:]); err != nil {
			break
		}
		isLast := (blockHeader[0] & 0x80) != 0
		blockType := blockHeader[0] & 0x7F
		blockLen := int(blockHeader[1])<<16 | int(blockHeader[2])<<8 | int(blockHeader[3])

		if blockLen <= 0 || blockLen > 30*1024*1024 {
			break
		}

		if blockType == 6 {
			buf := make([]byte, blockLen)
			if _, err := io.ReadFull(f, buf); err != nil {
				break
			}
			if img, mime := parseFLACPictureBlock(buf); len(img) > 0 {
				return img, mime
			}
		} else {
			if _, err := f.Seek(int64(blockLen), io.SeekCurrent); err != nil {
				break
			}
		}

		if isLast {
			break
		}
	}

	return nil, ""
}

func parseFLACPictureBlock(buf []byte) ([]byte, string) {
	if len(buf) < 32 {
		return nil, ""
	}
	pos := 4 // Пропускаем picture type (uint32)
	mimeLen := int(binary.BigEndian.Uint32(buf[pos : pos+4]))
	pos += 4
	if mimeLen < 0 || pos+mimeLen+4 > len(buf) {
		return parseImageSignature(buf)
	}
	mimeStr := string(buf[pos : pos+mimeLen])
	pos += mimeLen

	descLen := int(binary.BigEndian.Uint32(buf[pos : pos+4]))
	pos += 4
	if descLen < 0 || pos+descLen+20 > len(buf) {
		return parseImageSignature(buf)
	}
	pos += descLen + 16 // Пропускаем width, height, depth, colors (4 * uint32)

	dataLen := int(binary.BigEndian.Uint32(buf[pos : pos+4]))
	pos += 4
	if dataLen <= 0 || pos+dataLen > len(buf) {
		return parseImageSignature(buf)
	}

	imgData := buf[pos : pos+dataLen]
	if mimeStr == "" {
		mimeStr = "image/jpeg"
	}
	return imgData, mimeStr
}

func parseImageSignature(buf []byte) ([]byte, string) {
	// Ищем сигнатуру JPEG (FF D8 FF)
	if idx := bytes.Index(buf, []byte{0xFF, 0xD8, 0xFF}); idx != -1 && len(buf)-idx > 64 {
		return buf[idx:], "image/jpeg"
	}
	// Ищем сигнатуру PNG (89 50 4E 47 0D 0A 1A 0A)
	if idx := bytes.Index(buf, []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}); idx != -1 && len(buf)-idx > 64 {
		return buf[idx:], "image/png"
	}
	return nil, ""
}

func decodeSynchsafeInt(b []byte) int {
	if len(b) < 4 {
		return 0
	}
	return (int(b[0]&0x7F) << 21) | (int(b[1]&0x7F) << 14) | (int(b[2]&0x7F) << 7) | int(b[3]&0x7F)
}

func extractCoverViaFFmpeg(audioPath string) ([]byte, string) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return nil, ""
	}
	cmd := exec.Command("ffmpeg", "-nostdin", "-v", "error", "-i", audioPath, "-an", "-vcodec", "mjpeg", "-vframes", "1", "-f", "image2pipe", "-")
	out, err := cmd.Output()
	if err == nil && len(out) > 64 {
		return out, "image/jpeg"
	}
	return nil, ""
}
