// Package ai реализует облачные AI-пайплайны сервиса poisq: мультиязычную очистку
// и консенсус текстов через Gemini 2.5 Flash, сжатие аудио на лету в моно-Opus
// и двухэтапную генерацию синхронизированного караоке через Groq Whisper + Gemini.
//
// Данный файл lyrics_cleaner.go отвечает за мультиязычную валидацию, определение языка (ISO 639-1),
// очистку текстов от встроенных переводов (например, китайских строк из NetEase), транслита,
// структурных пометок ([Куплет 1], [Припев x2]) и слияние лучших таймкодов с эталонной орфографией.
package ai

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/F5GO/poisq/internal/lyrics"
)

var (
	// Быстрая локальная регулярка для снятия структурных пометок [Куплет 1], (Припев x2) и т.д.
	structuralTagRegex = regexp.MustCompile(`(?im)^\s*[\[\(]\s*(?:куплет|припев|бридж|интро|аутро|проигрыш|соло|verse|chorus|bridge|intro|outro|hook|pre-chorus|interlude)[^\]\)]*[\]\)]\s*$`)
)

// ConsensusOutput содержит результат AI-очистки и консенсуса от Gemini.
type ConsensusOutput struct {
	LangCode      string `json:"langCode"`
	CleanedLyrics string `json:"cleanedLyrics"`
	IsSynced      bool   `json:"isSynced"`

	// Служебные поля качества (не приходят от Gemini): сколько строк итогового LRC и сколько из них
	// получили время без подтверждения реальным словом в аудио.
	TotalLines       int `json:"-"`
	UnconfirmedLines int `json:"-"`
}

// LyricsCleaner выполняет локальную и облачную (Gemini) очистку и консенсус текстов песен.
type LyricsCleaner struct {
	gemini TextGenerator
}

// NewLyricsCleaner создает новый экземпляр очистителя и валидатора текстов.
func NewLyricsCleaner(gemini TextGenerator) *LyricsCleaner {
	return &LyricsCleaner{gemini: gemini}
}

// FastCleanStructuralTags локально удаляет строки вида [Куплет 1], [Chorus], сохраняя LRC таймкоды.
func FastCleanStructuralTags(raw string) string {
	lines := strings.Split(raw, "\n")
	var out []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// Проверяем текст строки без LRC-таймкода
		textOnly := strings.TrimSpace(lyrics.StripLRCTimestamps(trimmed))
		if structuralTagRegex.MatchString(textOnly) {
			continue
		}
		out = append(out, trimmed)
	}
	return strings.Join(out, "\n")
}

// BuildConsensus объединяет кандидатов от разных провайдеров через Gemini 2.5 Flash:
// удаляет мусорные переводы (NetEase CN/EN), романизацию, пометки [Куплет 1] и объединяет
// лучшие таймкоды с эталонной орфографией.
func (c *LyricsCleaner) BuildConsensus(ctx context.Context, artist, title string, candidates []lyrics.TournamentCandidate) (*ConsensusOutput, error) {
	if len(candidates) == 0 {
		return nil, fmt.Errorf("нет кандидатов для AI-консенсуса")
	}

	systemPrompt := `Ты — экспертный лингвист и редактор синхронизированных текстов песен (LRC) для мультиязычной медиатеки (RU, EN, DE, FR, JP, ES, UA и др.).
Твоя задача:
1. Определить основной язык оригинала песни (двухбуквенный код ISO 639-1: "ru", "en", "de", "fr", "ja", "es", "uk" и т.д.).
2. Очистить текст от мусора:
   - Удалить встроенные построчные переводы на другие языки (особенно китайские переводы из NetEase 163.com).
   - Удалить транслитерацию (романизацию), если есть оригинальная письменность (кириллица, кандзи/кана и т.д.).
   - Удалить служебные пометки структуры: [Куплет 1], [Припев], (Припев x2), [Verse 1], [Chorus], кредиты авторов (Lyrics by, Composed by) в начале текста.
3. Если среди вариантов есть синхронизированный LRC ([mm:ss.xx]) и чистый эталонный текст (например, из Genius или Яндекс Музыки), объедини точные таймкоды [mm:ss.xx] с безупречной орфографией и пунктуацией эталона.
4. Верни ответ СТРОГО в формате JSON по схеме:
{"langCode": "ru", "isSynced": true, "cleanedLyrics": "[00:12.34] Первая строка\n[00:16.50] Вторая строка"}`

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Артист: %s\nНазвание: %s\n\nВарианты от провайдеров:\n", artist, title))
	for i, cand := range candidates {
		if i >= 3 {
			break // Передаем топ-3 кандидата для экономии токенов и высокой скорости
		}
		text := cand.Lyrics.SyncedLyrics
		if text == "" {
			text = cand.Lyrics.PlainLyrics
		}
		sb.WriteString(fmt.Sprintf("\n--- Источник %d: %s (Формат: %s, Балл: %.1f) ---\n%s\n",
			i+1, cand.ProviderID, cand.Lyrics.Kind, cand.Score.Total, text))
	}

	respJSON, err := c.gemini.GenerateText(ctx, systemPrompt, sb.String(), "application/json")
	if err != nil {
		return nil, err
	}

	return ParseConsensusOutput(respJSON)
}
