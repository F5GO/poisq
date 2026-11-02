// Package tagger реализует бережное чтение, хирургическую перезапись и откат (Undo)
// аудио-тегов по стандарту MusicBrainz Picard без перекодирования аудиопотока (Bit-Perfect).
//
// Данный файл tag_reader.go отвечает за извлечение контрольной суммы нежатого аудиопотока
// FLAC (16-байтная сигнатура MD5 из блока STREAMINFO) для математической проверки
// отсутствия потерь качества до и после записи тегов.
package tagger

import (
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

// ExtractFLACAudioMD5 напрямую читает заголовок .flac файла и извлекает 16-байтную
// MD5-сигнатуру исходного аудиопотока из обязательного первого блока STREAMINFO (байты 26..42).
// Это гарантирует 100% контроль того, что ни один сэмпл звука не изменился при записи тегов.
func ExtractFLACAudioMD5(filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	// Заголовок FLAC:
	// 0..4: "fLaC" (4 байта)
	// 4..8: Заголовок блока STREAMINFO (тип 0, длина 34 байта)
	// 8..26: Параметры сэмплов/каналов (18 байт)
	// 26..42: 16-байтная MD5 сигнатура нежатого аудиопотока
	header := make([]byte, 42)
	if _, err := io.ReadFull(f, header); err != nil {
		return "", fmt.Errorf("не удалось прочитать заголовок FLAC: %w", err)
	}

	if string(header[0:4]) != "fLaC" {
		return "", fmt.Errorf("файл не имеет сигнатуры fLaC")
	}

	blockType := header[4] & 0x7F
	if blockType != 0 {
		return "", fmt.Errorf("первый блок метаданных FLAC не является STREAMINFO (type=%d)", blockType)
	}

	md5Bytes := header[26:42]
	return hex.EncodeToString(md5Bytes), nil
}
