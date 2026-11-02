// Package ai реализует облачные AI-пайплайны сервиса poisq: мультиязычную очистку
// и консенсус текстов через Gemini 2.5 Flash, сжатие аудио на лету в моно-Opus
// и двухэтапную генерацию синхронизированного караоке через Groq Whisper + Gemini.
//
// Данный файл karaoke_merger.go реализует двухэтапный гибридный облачный пайплайн караоке:
// 1. Аудио сжимается в моно-Opus и отправляется в Groq Whisper (whisper-large-v3-turbo).
// 2. Акустические таймкоды Whisper передаются вместе с чистым эталонным текстом в Gemini 2.5 Flash,
//    который исправляет любые оговорки распознавания по эталону с сохранением миллисекундной привязки.
package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// KaraokePipeline координирует генерацию синхронизированного LRC из аудиофайла и эталонного текста.
type KaraokePipeline struct {
	transcoder *AudioTranscoder
	whisper    *GroqWhisperClient
	gemini     *GeminiClient
}

// NewKaraokePipeline создает новый экземпляр генератора караоке.
func NewKaraokePipeline(transcoder *AudioTranscoder, whisper *GroqWhisperClient, gemini *GeminiClient) *KaraokePipeline {
	return &KaraokePipeline{
		transcoder: transcoder,
		whisper:    whisper,
		gemini:     gemini,
	}
}

// GenerateSyncedLRC выполняет полный цикл: ffmpeg (Opus) -> Groq Whisper -> Gemini 2.5 Flash -> эталонный LRC.
func (kp *KaraokePipeline) GenerateSyncedLRC(ctx context.Context, audioFilePath, artist, title, referencePlainLyrics, langHint string) (*ConsensusOutput, error) {
	// Шаг 1: Сжатие аудио в памяти в легкий моно-Opus (16 kHz, 32 kbps)
	opusBytes, err := kp.transcoder.TranscodeToOpusMono16k(ctx, audioFilePath)
	if err != nil {
		return nil, fmt.Errorf("этап 1 (ffmpeg): %w", err)
	}

	// Шаг 2: Получение акустических таймкодов от Groq Whisper-large-v3-turbo
	whisperRes, err := kp.whisper.TranscribeAudio(ctx, opusBytes, langHint)
	if err != nil {
		return nil, fmt.Errorf("этап 2 (Groq Whisper): %w", err)
	}

	// Компактно сериализуем сегменты и слова Whisper для передачи в Gemini
	acousticJSON, err := json.Marshal(map[string]any{
		"language": whisperRes.Language,
		"duration": whisperRes.Duration,
		"segments": whisperRes.Segments,
		"words":    whisperRes.Words,
	})
	if err != nil {
		return nil, err
	}

	// Шаг 3: Мердж эталонного текста с физическими таймкодами Whisper через Gemini 2.5 Flash
	systemPrompt := `Ты — высокоточный движок синхронизации караоке (Audio-to-Lyrics Aligner).
Тебе переданы:
1) Акустические таймкоды реального звучания вокала от Whisper (segments и words с точностью до долей секунды).
2) Чистый эталонный текст песни (если он был найден в базах).

Твоя задача:
- Сопоставить строчки эталонного текста с физическими таймкодами Whisper.
- Заменить любые возможные ослышки или оговорки Whisper на 100% правильные слова из эталонного текста, сохранив точную миллисекундную привязку к началу звучания каждой строчки [mm:ss.xx].
- Удалить любые пометки [Куплет], [Припев], переводы и мусор.
- Вернуть результат СТРОГО в JSON формате:
{"langCode": "ru", "isSynced": true, "cleanedLyrics": "[00:14.20] Первая строчка\n[00:18.05] Вторая строчка"}`

	userPrompt := fmt.Sprintf(
		"Артист: %s\nНазвание: %s\n\n=== ЭТАЛОННЫЙ ТЕКСТ ===\n%s\n\n=== ТАЙМКОДЫ WHISPER (JSON) ===\n%s",
		artist, title, strings.TrimSpace(referencePlainLyrics), string(acousticJSON),
	)

	respJSON, err := kp.gemini.GenerateText(ctx, systemPrompt, userPrompt, "application/json")
	if err != nil {
		return nil, fmt.Errorf("этап 3 (Gemini Merge): %w", err)
	}

	var out ConsensusOutput
	if err := json.Unmarshal([]byte(respJSON), &out); err != nil {
		return nil, fmt.Errorf("ошибка декодирования JSON от Gemini: %w", err)
	}
	out.CleanedLyrics = strings.TrimSpace(out.CleanedLyrics)
	if out.CleanedLyrics == "" {
		return nil, fmt.Errorf("Gemini вернул пустой LRC после мерджа с Whisper")
	}

	return &out, nil
}
