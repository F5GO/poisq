// Package ai реализует облачные AI-пайплайны сервиса poisq: мультиязычную очистку
// и консенсус текстов через Gemini 2.5 Flash, сжатие аудио на лету в моно-Opus
// и двухэтапную генерацию синхронизированного караоке через Groq Whisper + Gemini.
//
// Данный файл metadata_arbiter.go реализует интеллектуальный арбитраж метаданных и обложек
// с помощью Gemini 2.5 Flash. Определяет канонический альбом для треков-синглов (например,
// привязывает песню "Boulevard of Broken Dreams" к номерному альбому "American Idiot" Green Day),
// выявляет пиратские сборники и формирует точные поисковые запросы для Apple Music / iTunes.
package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// FolderContext описывает контекст файловой папки для анализа нейросетью.
type FolderContext struct {
	FolderPath      string   `json:"folder_path"`
	FolderName      string   `json:"folder_name"`
	TrackFiles      []string `json:"track_files"`
	ExistingArtists []string `json:"existing_artists"`
	ExistingAlbums  []string `json:"existing_albums"`
	ExistingTitles  []string `json:"existing_titles"`
}

// ArbiterDecision содержит структурированное решение нейросети о каноническом альбоме и обложке.
type ArbiterDecision struct {
	CanonicalArtist string `json:"canonical_artist"`
	CanonicalAlbum  string `json:"canonical_album"`
	ReleaseType     string `json:"release_type"` // "studio_album", "single", "ep", "soundtrack", "compilation", "live"
	TrackTitle      string `json:"track_title,omitempty"`
	CoverSearchTerm string `json:"cover_search_term"`
	Confidence      int    `json:"confidence"`
	Explanation     string `json:"explanation"`
}

// MetadataArbiter отвечает за разрешение спорных ситуаций с тегами и обложками через Gemini 2.5 Flash.
type MetadataArbiter struct {
	gen TextGenerator
}

// NewMetadataArbiter создает экземпляр арбитра метаданных.
func NewMetadataArbiter(gen TextGenerator) *MetadataArbiter {
	return &MetadataArbiter{gen: gen}
}

const arbiterSystemPrompt = `Ты — авторитетный музыкальный архивариус и эксперт каталогизации аудиоколлекций по стандартам MusicBrainz Picard, Apple Music и Discogs.
Твоя задача — проанализировать контекст аудиофайлов и папки (путь, имена файлов, теги) и разрешить спорную ситуацию с метаданными и обложкой.

Ключевые правила арбитража:
1. Если папка содержит отдельный трек/сингл (например, "Green Day – Boulevard of Broken Dreams" или "01 - Boulevard of Broken Dreams.mp3"), ты ДОЛЖЕН определить и указать канонический студийный номерной альбом артиста, в который изначально входит этот трек (например, "American Idiot" для Green Day), а не повторять название песни как альбом.
2. Определи точный тип релиза (release_type):
   - "studio_album" (официальный студийный номерной альбом)
   - "single" (отдельный коммерческий сингл)
   - "ep" (мини-альбом)
   - "soundtrack" (саундтрек к фильму/игре)
   - "compilation" (сборник лучших хитов / антология)
   - "live" (концертная запись)
3. Сформируй идеальный поисковый запрос (cover_search_term) для Apple Music / iTunes (например, "Green Day American Idiot"), который гарантированно вернет подлинную оригинальную обложку студийного альбома без караоке, трибьютов, каверов и минусовок.
4. Дай краткое, информативное объяснение решения (explanation) на русском языке (1-2 предложения).

Ответ верни СТРОГО в формате валидного JSON без markdown-разметки:
{
  "canonical_artist": "Имя исполнителя",
  "canonical_album": "Канонический номерной альбом",
  "release_type": "studio_album | single | ep | soundtrack | compilation | live",
  "track_title": "Название трека",
  "cover_search_term": "Поисковый запрос для обложки в iTunes",
  "confidence": 95,
  "explanation": "Объяснение на русском языке"
}`

// ResolveDisputedMetadata анализирует контекст файлов и папки и возвращает решение нейросети.
func (a *MetadataArbiter) ResolveDisputedMetadata(ctx context.Context, fCtx FolderContext) (*ArbiterDecision, error) {
	if a.gen == nil {
		return nil, fmt.Errorf("LLM is not initialized")
	}

	var userPromptBuilder strings.Builder
	userPromptBuilder.WriteString(fmt.Sprintf("Папка: %s\n", fCtx.FolderName))
	userPromptBuilder.WriteString(fmt.Sprintf("Полный путь: %s\n", fCtx.FolderPath))

	if len(fCtx.TrackFiles) > 0 {
		userPromptBuilder.WriteString("Файлы в папке:\n")
		limit := len(fCtx.TrackFiles)
		if limit > 10 {
			limit = 10
		}
		for i := 0; i < limit; i++ {
			userPromptBuilder.WriteString(fmt.Sprintf("- %s\n", fCtx.TrackFiles[i]))
		}
	}

	if len(fCtx.ExistingArtists) > 0 {
		userPromptBuilder.WriteString(fmt.Sprintf("Текущие артисты в тегах: %s\n", strings.Join(fCtx.ExistingArtists, ", ")))
	}
	if len(fCtx.ExistingAlbums) > 0 {
		userPromptBuilder.WriteString(fmt.Sprintf("Текущие альбомы в тегах: %s\n", strings.Join(fCtx.ExistingAlbums, ", ")))
	}
	if len(fCtx.ExistingTitles) > 0 {
		userPromptBuilder.WriteString(fmt.Sprintf("Текущие названия треков: %s\n", strings.Join(fCtx.ExistingTitles, ", ")))
	}

	rawJSON, err := a.gen.GenerateText(ctx, arbiterSystemPrompt, userPromptBuilder.String(), "application/json")
	if err != nil {
		return nil, fmt.Errorf("ошибка вызова Gemini Metadata Arbiter: %w", err)
	}

	cleanJSON := strings.TrimSpace(rawJSON)
	cleanJSON = strings.TrimPrefix(cleanJSON, "```json")
	cleanJSON = strings.TrimPrefix(cleanJSON, "```")
	cleanJSON = strings.TrimSuffix(cleanJSON, "```")
	cleanJSON = strings.TrimSpace(cleanJSON)

	var decision ArbiterDecision
	if err := json.Unmarshal([]byte(cleanJSON), &decision); err != nil {
		return nil, fmt.Errorf("ошибка парсинга JSON ответа Gemini (%s): %w", cleanJSON, err)
	}

	if decision.CanonicalArtist == "" && len(fCtx.ExistingArtists) > 0 {
		decision.CanonicalArtist = fCtx.ExistingArtists[0]
	}
	if decision.CoverSearchTerm == "" {
		decision.CoverSearchTerm = strings.TrimSpace(decision.CanonicalArtist + " " + decision.CanonicalAlbum)
	}
	if decision.Confidence <= 0 {
		decision.Confidence = 90
	}

	return &decision, nil
}
