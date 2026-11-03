// Package ai реализует облачные AI-пайплайны сервиса poisq: мультиязычную очистку
// и консенсус текстов через Gemini, сжатие аудио на лету в моно-Opus
// и двухэтапную генерацию синхронизированного караоке через Groq Whisper + Gemini.
//
// Данный файл karaoke_merger.go реализует модульный двухэтапный гибридный облачный пайплайн караоке:
// Этап 1-2: Сжатие в моно-Opus и получение акустических таймкодов от Groq Whisper (TranscribeToSegments).
//           Промежуточные акустические таймкоды кэшируются, что позволяет перезапускать задачу по этапам.
// Этап 3: Высокоточный мердж эталонного текста с физическими таймкодами через Google Gemini (MergeSegmentsWithReference).
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

// TranscribeToSegments выполняет этапы 1 и 2: сжатие в Opus и извлечение компактных таймкодов Groq Whisper.
func (kp *KaraokePipeline) TranscribeToSegments(ctx context.Context, audioFilePath, langHint string) (string, error) {
	// Шаг 1: Сжатие аудио в памяти в легкий моно-Opus (16 kHz, 32 kbps)
	opusBytes, err := kp.transcoder.TranscodeToOpusMono16k(ctx, audioFilePath)
	if err != nil {
		return "", fmt.Errorf("этап 1 (ffmpeg): %w", err)
	}

	// Шаг 2: Получение акустических таймкодов от Groq Whisper-large-v3-turbo
	whisperRes, err := kp.whisper.TranscribeAudio(ctx, opusBytes, langHint)
	if err != nil {
		return "", fmt.Errorf("этап 2 (Groq Whisper): %w", err)
	}

	// Оптимизация: передаем компактные сегменты (начало звучания фразы, конец и текст),
	// не раздувая промпт пословными массивами, что снижает размер полезной нагрузки в 30-50 раз.
	type compactSegment struct {
		Start float64 `json:"start"`
		End   float64 `json:"end"`
		Text  string  `json:"text"`
	}

	compactList := make([]compactSegment, 0, len(whisperRes.Segments))
	for _, s := range whisperRes.Segments {
		compactList = append(compactList, compactSegment{
			Start: s.Start,
			End:   s.End,
			Text:  strings.TrimSpace(s.Text),
		})
	}

	acousticBytes, err := json.Marshal(map[string]any{
		"language": whisperRes.Language,
		"duration": whisperRes.Duration,
		"segments": compactList,
	})
	if err != nil {
		return "", err
	}

	return string(acousticBytes), nil
}

// MergeSegmentsWithReference выполняет этап 3: сопоставление эталонного текста с физическими таймкодами через Google Gemini.
func (kp *KaraokePipeline) MergeSegmentsWithReference(ctx context.Context, acousticJSON, artist, title, referencePlainLyrics string) (*ConsensusOutput, error) {
	systemPrompt := `Ты — высокоточный движок синхронизации караоке (Audio-to-Lyrics Aligner).
Тебе переданы:
1) Акустические таймкоды реального звучания вокала от Whisper (segments с точностью до долей секунды).
2) Чистый эталонный текст песни (если он был найден в базах).

Твоя задача:
- Сопоставить строчки эталонного текста с физическими таймкодами Whisper.
- Заменить любые возможные ослышки или оговорки Whisper на 100% правильные слова из эталонного текста, сохранив точную миллисекундную привязку к началу звучания каждой строчки [mm:ss.xx].
- Удалить любые пометки [Куплет], [Припев], переводы и мусор.
- Вернуть результат СТРОГО в JSON формате:
{"langCode": "ru", "isSynced": true, "cleanedLyrics": "[00:14.20] Первая строчка\n[00:18.05] Вторая строчка"}`

	userPrompt := fmt.Sprintf(
		"Артист: %s\nНазвание: %s\n\n=== ЭТАЛОННЫЙ ТЕКСТ ===\n%s\n\n=== ТАЙМКОДЫ WHISPER (JSON) ===\n%s",
		artist, title, strings.TrimSpace(referencePlainLyrics), strings.TrimSpace(acousticJSON),
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

// GenerateSyncedLRC выполняет полный сквозной цикл генерации караоке.
func (kp *KaraokePipeline) GenerateSyncedLRC(ctx context.Context, audioFilePath, artist, title, referencePlainLyrics, langHint string) (*ConsensusOutput, error) {
	acousticJSON, err := kp.TranscribeToSegments(ctx, audioFilePath, langHint)
	if err != nil {
		return nil, err
	}
	return kp.MergeSegmentsWithReference(ctx, acousticJSON, artist, title, referencePlainLyrics)
}
