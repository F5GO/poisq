// Package ai реализует облачные AI-пайплайны сервиса poisq.
//
// Данный файл query_expander.go реализует «ИИ-умный поиск» текстов: LLM (Groq, запасной — Gemini)
// подсказывает, как исполнитель и песня могут быть записаны в базах LRCLIB/Genius/Яндекс Музыка/NetEase
// («МакSим» → «MakSim», «Sимптом» → «Symptom»). Подсказки используются ТОЛЬКО как поисковые запросы:
// принять текст из базы можно лишь после проверки фильтром соответствия и согласия нескольких источников,
// поэтому ошибка нейросети не может подменить песню. Ответы кэшируются в SQLite, повторный вызов бесплатен.
package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/F5GO/poisq/internal/logger"
	"github.com/F5GO/poisq/internal/lyrics"
)

// AliasCache — хранилище кэша подсказок (реализуется db.AliasCacheRepository).
type AliasCache interface {
	Get(key string) (string, bool)
	Set(key, valueJSON string) error
}

// LLMQueryExpander реализует lyrics.QueryExpander на основе LLM.
type LLMQueryExpander struct {
	gen   TextGenerator
	cache AliasCache
}

// NewLLMQueryExpander создаёт расширитель запросов. cache может быть nil.
func NewLLMQueryExpander(gen TextGenerator, cache AliasCache) *LLMQueryExpander {
	return &LLMQueryExpander{gen: gen, cache: cache}
}

type nameSuggestions struct {
	ArtistNames []string `json:"artist_names"`
	TitleNames  []string `json:"title_names"`
}

const queryExpanderSystemPrompt = `Ты — эксперт по каталогизации музыки. Тебе дают исполнителя, название песни и альбом из тегов аудиофайла.
Нужно перечислить, под какими написаниями эта песня может числиться в базах текстов песен (LRCLIB, Genius, Яндекс Музыка, NetEase):
- artist_names: до 6 реально существующих вариантов имени исполнителя: официальное (в том числе стилизованное, например "МакSим"), на оригинальном языке, латиницей ("MakSim"), распространённые транслитерации и варианты без "feat.". Не выдумывай несуществующие имена.
- title_names: оставь пустым списком (переводы и выдуманные названия не нужны).
Если не уверен — верни меньше вариантов. Ответ СТРОГО в виде JSON-объекта:
{"artist_names": ["..."], "title_names": []}`

func cleanSuggestions(in []string, original string) []string {
	seen := map[string]struct{}{strings.ToLower(strings.TrimSpace(original)): {}}
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || len([]rune(s)) > 80 || strings.ContainsAny(s, "\n\r\t{}[]") {
			continue
		}
		k := strings.ToLower(s)
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, s)
		if len(out) >= 6 {
			break
		}
	}
	return out
}

// ExpandQuery реализует lyrics.QueryExpander: возвращает дополнительные запросы (без исходного).
func (e *LLMQueryExpander) ExpandQuery(ctx context.Context, q lyrics.SearchQuery) ([]lyrics.SearchQuery, error) {
	if e == nil || e.gen == nil {
		return nil, fmt.Errorf("LLM не настроена")
	}
	key := "q:" + strings.ToLower(strings.TrimSpace(q.Artist)) + "|" + strings.ToLower(strings.TrimSpace(q.Title))

	var sug nameSuggestions
	cached := false
	if e.cache != nil {
		if v, ok := e.cache.Get(key); ok && json.Unmarshal([]byte(v), &sug) == nil {
			cached = true
			logger.Debug("[AI-SEARCH] Подсказки из кэша для «%s — %s»", q.Artist, q.Title)
		}
	}

	if !cached {
		started := time.Now()
		user := fmt.Sprintf("Исполнитель: %s\nНазвание: %s\nАльбом: %s\n", q.Artist, q.Title, q.Album)
		raw, err := e.gen.GenerateText(ctx, queryExpanderSystemPrompt, user, "application/json")
		if err != nil {
			return nil, err
		}
		raw = strings.TrimSpace(raw)
		raw = strings.TrimPrefix(raw, "```json")
		raw = strings.TrimPrefix(raw, "```")
		raw = strings.TrimSuffix(raw, "```")
		if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &sug); err != nil {
			return nil, fmt.Errorf("не удалось разобрать ответ LLM: %w", err)
		}
		sug.ArtistNames = cleanSuggestions(sug.ArtistNames, "")
		sug.TitleNames = cleanSuggestions(sug.TitleNames, "")
		if e.cache != nil {
			if b, err := json.Marshal(sug); err == nil {
				if err := e.cache.Set(key, string(b)); err != nil {
					logger.Warn("[AI-SEARCH] Не удалось записать кэш подсказок: %v", err)
				}
			}
		}
		logger.Info("[AI-SEARCH] LLM за %s предложила: артисты=%v названия=%v", time.Since(started).Round(time.Millisecond), sug.ArtistNames, sug.TitleNames)
	}

	var out []lyrics.SearchQuery
	for _, a := range cleanSuggestions(sug.ArtistNames, q.Artist) {
		v := q
		v.Artist = a
		out = append(out, v)
	}
	for _, t := range cleanSuggestions(sug.TitleNames, q.Title) {
		v := q
		v.Title = t
		out = append(out, v)
	}
	return out, nil
}
