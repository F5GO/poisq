// Package ai реализует облачные AI-пайплайны сервиса poisq: мультиязычную очистку
// и консенсус текстов через Gemini, сжатие аудио на лету в моно-Opus
// и двухэтапную генерацию синхронизированного караоке через Groq Whisper + Gemini.
//
// Что делает этот код и для чего он нужен:
// Данный файл karaoke_merger.go реализует модульный бронебойный пайплайн генерации караоке (.lrc):
// Этап 1-2: Сжатие в моно-Opus с частотным вокальным фильтром и получение пословных DTW-таймкодов
//
//	от полной 32-слойной модели Groq Whisper (whisper-large-v3) с калибровкой растянутых интро.
//
// Этап 3: Глобальное динамическое программирование Нидлмана — Вунша (Needleman-Wunsch) +
//
//	выверка слов через Google Gemini + жесткая пост-привязка таймкодов (SnapLRCToAcousticWords)
//	к физическим словам аудиозаписи, исключающая любые галлюцинации времени со стороны LLM.
package ai

import (
	"context"
	"fmt"
	"strings"

	"github.com/F5GO/poisq/internal/logger"
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

// TranscribeToSegments выполняет этапы 1 и 2: сжатие в Opus и извлечение откалиброванных пословных таймкодов Groq Whisper.
func (kp *KaraokePipeline) TranscribeToSegments(ctx context.Context, audioFilePath, langHint string, promptHint ...string) (string, error) {
	// Шаг 1: Сжатие аудио в памяти в легкий моно-Opus (16 kHz, 32 kbps) с вокальным фильтром 100-7500 Гц
	opusBytes, err := kp.transcoder.TranscodeToOpusMono16k(ctx, audioFilePath)
	if err != nil {
		return "", fmt.Errorf("этап 1 (ffmpeg): %w", err)
	}

	// Шаг 2: Получение пословных акустических таймкодов от Groq Whisper-large-v3
	whisperRes, err := kp.whisper.TranscribeAudio(ctx, opusBytes, langHint, promptHint...)
	if err != nil {
		return "", fmt.Errorf("этап 2 (Groq Whisper): %w", err)
	}

	return BuildAcousticPayload(whisperRes)
}

// MergeSegmentsWithReference выполняет этап 3:
//  1. Вычисляет глобальное выравнивание последовательностей Нидлмана — Вунша (Needleman-Wunsch) по словам Whisper.
//  2. Выполняет лингвистическую сверку через Google Gemini.
//  3. Применяет жесткий пост-фильтр SnapLRCToAcousticWords, привязывающий таймкод каждой строки
//     к физическому началу первого спетого слова в аудиодорожке.
func (kp *KaraokePipeline) MergeSegmentsWithReference(ctx context.Context, acousticJSON, artist, title, referencePlainLyrics string) (*ConsensusOutput, error) {
	wordStream, timelineData := FormatWordLevelStream(acousticJSON)
	preAlignedLRC := PreAlignReferenceWithWords(referencePlainLyrics, timelineData)

	systemPrompt := `Ты — эталонный акустический синхронизатор караоке (Forced Audio-to-Lyrics Aligner).
Тебе переданы:
1) ЧЕРНОВИК LRC С МАТЕМАТИЧЕСКИМИ ТАЙМКОДАМИ (рассчитанный алгоритмом глобального выравнивания Нидлмана — Вунша по реальным словам аудиодорожки).
2) АКУСТИЧЕСКИЕ ВОКАЛЬНЫЕ ФРАЗЫ И ДЕТАЛЬНАЯ ПОСЛОВНАЯ СЕТКА WHISPER ([mm:ss.xx]слово), где перед каждым словом стоит его откалиброванный физический таймкод вступления голоса.

КРИТИЧЕСКИЕ ПРАВИЛА СИНХРОНИЗАЦИИ:
1. ЖЕЛЕЗНАЯ ПРИВЯЗКА К АУДИО: Таймкод [mm:ss.xx] каждой строки обязан СТРОГО совпадать с таймкодом ПЕРВОГО СЛОВА этой строки из пословной сетки Whisper. Категорически запрещено придумывать равномерный шаг (+2..4 сек) в уме!
2. УЧЕТ РЕАЛЬНОЙ СТРУКТУРЫ ПЕСНИ: Если в аудиозаписи (в акустических фразах Whisper) припев или строчка поется больше или меньше раз, чем в эталонном тексте, формируй строки строго по тому, что РЕАЛЬНО ПОЕТСЯ в аудио!
3. ИСПРАВЛЕНИЕ ОСЛЫШЕК: В местах, где Whisper ослышался на фоне музыки, подставляй правильные слова из эталонного текста, но СОХРАНЯЙ точный таймкод от соответствующего слова Whisper.
4. ЗАЩИТА ОТ ИНСТРУМЕНТАЛЬНОГО ИНТРО: Первая строка никогда не может начинаться раньше времени первого слова вокала.
5. ЧИСТОТА: Удали любые теги [Куплет], [Припев], [Verse], [Chorus].
6. Верни результат СТРОГО в виде валидного JSON объекта:
{
  "langCode": "ru",
  "isSynced": true,
  "cleanedLyrics": "[00:14.20] Первая строчка\n[00:18.05] Вторая строчка"
}`

	var userPrompt string
	if strings.TrimSpace(preAlignedLRC) != "" {
		userPrompt = fmt.Sprintf(
			"Артист: %s\nНазвание: %s\n\n=== ЭТАЛОННЫЙ ТЕКСТ (ВЫРОВНЕННЫЙ ГЛОБАЛЬНЫМ DP ПО СЛОВАМ АУДИО) ===\n%s\n\n=== АКУСТИЧЕСКИЕ ДАННЫЕ WHISPER ===\n%s",
			artist, title, preAlignedLRC, wordStream,
		)
	} else {
		directLRC := BuildDirectAcousticLRC(timelineData)
		userPrompt = fmt.Sprintf(
			"Артист: %s\nНазвание: %s\n\nЭталонный текст в базах отсутствует. Исправь только возможные опечатки/ослышки в словах и пунктуацию в следующих акустических строках, СТРОГО СОХРАНЯЯ таймкод [mm:ss.xx] первого слова каждой строки:\n\n=== АКУСТИЧЕСКИЙ LRC ИЗ ГОЛОСА ===\n%s\n\n=== ПОСЛОВНАЯ СЕТКА WHISPER ===\n%s",
			artist, title, directLRC, wordStream,
		)
	}

	refLines := 0
	for _, l := range strings.Split(referencePlainLyrics, "\n") {
		if strings.TrimSpace(l) != "" {
			refLines++
		}
	}
	logger.Info("[KARAOKE] Мердж «%s — %s»: строк эталона %d, слов Whisper %d, предвыравненный черновик: %v",
		artist, title, refLines, func() int {
			if timelineData == nil {
				return 0
			}
			return len(timelineData.Words)
		}(), strings.TrimSpace(preAlignedLRC) != "")

	respJSON, err := kp.gemini.GenerateText(ctx, systemPrompt, userPrompt, "application/json")
	if err != nil {
		return nil, fmt.Errorf("этап 3 (Gemini Merge): %w", err)
	}

	out, err := ParseConsensusOutput(respJSON)
	if err != nil {
		return nil, fmt.Errorf("этап 3 (Gemini Merge): %w", err)
	}

	// БРОНЕБОЙНЫЙ ПОСТ-ФИЛЬТР:
	// Принудительно примагничиваем таймкоды всех строк ответа Gemini к реальным физическим словам
	// аудиозаписи через глобальное выравнивание Нидлмана — Вунша, исключая любую отсебятину LLM по времени!
	if timelineData != nil && len(timelineData.Words) > 0 {
		snapped := SnapLRCToAcousticWords(out.CleanedLyrics, timelineData)
		if strings.TrimSpace(snapped) != "" {
			out.CleanedLyrics = snapped
		}
	}

	outLines := 0
	for _, l := range strings.Split(out.CleanedLyrics, "\n") {
		if strings.TrimSpace(l) != "" {
			outLines++
		}
	}
	logger.Info("[KARAOKE] Итог: строк в LRC %d (эталон %d), язык=%s, синхронизирован=%v", outLines, refLines, out.LangCode, out.IsSynced)
	return out, nil
}

// GenerateSyncedLRC выполняет полный сквозной цикл генерации караоке.
func (kp *KaraokePipeline) GenerateSyncedLRC(ctx context.Context, audioFilePath, artist, title, referencePlainLyrics, langHint string) (*ConsensusOutput, error) {
	hint := strings.TrimSpace(artist + " - " + title)
	acousticJSON, err := kp.TranscribeToSegments(ctx, audioFilePath, langHint, hint)
	if err != nil {
		return nil, err
	}
	return kp.MergeSegmentsWithReference(ctx, acousticJSON, artist, title, referencePlainLyrics)
}
