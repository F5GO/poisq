// Package ai реализует облачные AI-пайплайны сервиса poisq: мультиязычную очистку
// и консенсус текстов через Gemini, сжатие аудио на лету в моно-Opus
// и двухэтапную генерацию синхронизированного караоке через Groq Whisper + Gemini.
//
// Что делает этот код и для чего он нужен:
// Данный файл karaoke_merger.go реализует модульный двухэтапный гибридный облачный пайплайн караоке:
// Этап 1-2: Сжатие в моно-Opus и получение пословных (word-level DTW) акустических таймкодов от Groq Whisper (TranscribeToSegments).
//           В Whisper передается текстовая подсказка (prompt) для исключения галлюцинаций на музыке.
// Этап 3: Локальное математическое выравнивание (Forced Alignment) + чистовая синхронизация через Google Gemini (MergeSegmentsWithReference).
package ai

import (
	"context"
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

// TranscribeToSegments выполняет этапы 1 и 2: сжатие в Opus и извлечение пословных таймкодов Groq Whisper.
func (kp *KaraokePipeline) TranscribeToSegments(ctx context.Context, audioFilePath, langHint string, promptHint ...string) (string, error) {
	// Шаг 1: Сжатие аудио в памяти в легкий моно-Opus (16 kHz, 32 kbps)
	opusBytes, err := kp.transcoder.TranscodeToOpusMono16k(ctx, audioFilePath)
	if err != nil {
		return "", fmt.Errorf("этап 1 (ffmpeg): %w", err)
	}

	// Шаг 2: Получение пословных акустических таймкодов от Groq Whisper-large-v3-turbo
	whisperRes, err := kp.whisper.TranscribeAudio(ctx, opusBytes, langHint, promptHint...)
	if err != nil {
		return "", fmt.Errorf("этап 2 (Groq Whisper): %w", err)
	}

	return BuildAcousticPayload(whisperRes)
}

// MergeSegmentsWithReference выполняет этап 3: локальное математическое выравнивание по словам
// и финальное сопоставление эталонного текста с пословными таймкодами через Google Gemini.
func (kp *KaraokePipeline) MergeSegmentsWithReference(ctx context.Context, acousticJSON, artist, title, referencePlainLyrics string) (*ConsensusOutput, error) {
	wordStream, timelineData := FormatWordLevelStream(acousticJSON)
	preAlignedRef := PreAlignReferenceWithWords(referencePlainLyrics, timelineData)

	systemPrompt := `Ты — эталонный акустический синхронизатор караоке (Forced Audio-to-Lyrics Aligner).
Тебе переданы:
1) ПОСЛОВНАЯ АКУСТИЧЕСКАЯ ЛЕНТА от Whisper, где ПЕРЕД КАЖДЫМ СЛОВОМ стоит его физический таймкод [mm:ss.xx] реального звучания вокала.
2) ЭТАЛОННЫЙ ТЕКСТ ПЕСНИ, разбитый по строкам с предрассчитанными математическими якорями первого слова.

КРИТИЧЕСКИЕ ПРАВИЛА ТОЧНОЙ СИНХРОНИЗАЦИИ:
1. ПОСЛОВНАЯ ТОЧНОСТЬ: Таймкод [mm:ss.xx] каждой строки выходного LRC должен СТРОГО равняться таймкоду ПЕРВОГО СЛОВА этой строки из пословной ленты Whisper!
2. ЗАЩИТА ОТ ИНСТРУМЕНТАЛЬНОГО ИНТРО: Никогда не ставь [00:00.00] или ранние секунды, если первое реальное слово вокала в ленте Whisper начинается позже (например на [00:18.40]). Вокал начинается строго со времени первого слова!
3. ИСПРАВЛЕНИЕ ОСЛЫШЕК WHISPER: Whisper на фоне музыки иногда слышит созвучную бессмыслицу или пропускает слово. В выходном LRC пиши 100% правильные слова из ЭТАЛОННОГО ТЕКСТА, но таймкод бери от соответствующего созвучного слова из ленты Whisper!
4. СОХРАНЕНИЕ СТРУКТУРЫ СТРОК: Каждая строчка эталонного текста должна стать отдельной строкой [mm:ss.xx] в выходном LRC. Не склеивай несколько строк в одну и не пропускай повторяющиеся припевы. Таймкоды обязаны строго возрастать.
5. ЧИСТОТА: Удали любые пометки [Куплет], [Припев], [Verse], [Chorus].
6. Верни результат СТРОГО в виде валидного JSON объекта:
{
  "langCode": "ru",
  "isSynced": true,
  "cleanedLyrics": "[00:14.20] Первая строчка\n[00:18.05] Вторая строчка"
}`

	var userPrompt string
	if strings.TrimSpace(preAlignedRef) != "" {
		userPrompt = fmt.Sprintf(
			"Артист: %s\nНазвание: %s\n\n=== ЭТАЛОННЫЙ ТЕКСТ С МАТЕМАТИЧЕСКИМИ ЯКОРЯМИ ===\n%s\n\n=== ПОСЛОВНАЯ АКУСТИЧЕСКАЯ ЛЕНТА WHISPER ([mm:ss.xx]слово) ===\n%s",
			artist, title, preAlignedRef, wordStream,
		)
	} else {
		userPrompt = fmt.Sprintf(
			"Артист: %s\nНазвание: %s\n\nЭталонный текст отсутствует. Сформируй чистые песенные строки (по 4-8 слов по смыслу и паузам) из пословной ленты Whisper, поставив в начале каждой строки точный таймкод её первого слова:\n\n=== ПОСЛОВНАЯ АКУСТИЧЕСКАЯ ЛЕНТА WHISPER ([mm:ss.xx]слово) ===\n%s",
			artist, title, wordStream,
		)
	}

	respJSON, err := kp.gemini.GenerateText(ctx, systemPrompt, userPrompt, "application/json")
	if err != nil {
		return nil, fmt.Errorf("этап 3 (Gemini Merge): %w", err)
	}

	out, err := ParseConsensusOutput(respJSON)
	if err != nil {
		return nil, fmt.Errorf("этап 3 (Gemini Merge): %w", err)
	}

	return out, nil
}

// GenerateSyncedLRC выполняет полный сквозной цикл генерации караоке.
func (kp *KaraokePipeline) GenerateSyncedLRC(ctx context.Context, audioFilePath, artist, title, referencePlainLyrics, langHint string) (*ConsensusOutput, error) {
	hint := strings.TrimSpace(artist + " - " + title + "\n" + referencePlainLyrics)
	acousticJSON, err := kp.TranscribeToSegments(ctx, audioFilePath, langHint, hint)
	if err != nil {
		return nil, err
	}
	return kp.MergeSegmentsWithReference(ctx, acousticJSON, artist, title, referencePlainLyrics)
}
