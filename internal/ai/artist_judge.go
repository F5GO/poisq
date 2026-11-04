// Package ai реализует облачные AI-пайплайны сервиса poisq.
//
// Данный файл artist_judge.go реализует ИИ-судью для умного поиска текстов. Нейросеть НЕ придумывает имена:
// ей даётся список реальных записей, найденных в базах по названию и длительности, и она отвечает только на вопрос
// «какие из них принадлежат тому же исполнителю, что и трек из библиотеки» (с учётом транслитерации, стилизованных
// имён вроде «МакSим» и контекста альбома). При сомнении — пустой список.
package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/F5GO/poisq/internal/logger"
	"github.com/F5GO/poisq/internal/lyrics"
)

// LLMArtistJudge реализует lyrics.ArtistJudge.
type LLMArtistJudge struct {
	gen TextGenerator
}

// NewLLMArtistJudge создаёт судью на основе LLM.
func NewLLMArtistJudge(gen TextGenerator) *LLMArtistJudge {
	return &LLMArtistJudge{gen: gen}
}

const artistJudgeSystemPrompt = `Ты — эксперт по каталогизации музыки. Тебе дают трек из личной библиотеки (исполнитель, название, альбом, длительность)
и пронумерованный список записей, найденных в музыкальных базах по названию и длительности.
Определи, какие записи списка — это ТОТ ЖЕ исполнитель, что и в библиотеке (допустимы транслитерация, латиница вместо кириллицы,
стилизованное написание вроде "МакSим" = "MakSim" = "Максим", разные алфавиты и сокращения).
Правила: отвечай только по данным списка, ничего не выдумывай; если не уверен или это другой исполнитель (кавер, тёзка) — не включай запись.
Ответ СТРОГО в виде JSON-объекта: {"same": [номера записей]} — пустой список, если подходящих нет.`

// SameArtist реализует lyrics.ArtistJudge.
func (j *LLMArtistJudge) SameArtist(ctx context.Context, original lyrics.SearchQuery, cands []lyrics.DiscoveredTrack) ([]int, error) {
	if j == nil || j.gen == nil {
		return nil, fmt.Errorf("LLM не настроена")
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Трек из библиотеки: исполнитель=%q, название=%q, альбом=%q, длительность=%v\n\nЗаписи из баз:\n",
		original.Artist, original.Title, original.Album, original.Duration)
	for i, c := range cands {
		fmt.Fprintf(&sb, "%d. база=%s исполнитель=%q название=%q длительность=%v\n", i, c.Provider, c.Artist, c.Title, c.Duration)
	}
	raw, err := j.gen.GenerateText(ctx, artistJudgeSystemPrompt, sb.String(), "application/json")
	if err != nil {
		return nil, err
	}
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	var resp struct {
		Same []int `json:"same"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &resp); err != nil {
		return nil, fmt.Errorf("не удалось разобрать ответ ИИ-судьи: %w", err)
	}
	logger.Info("[AI-JUDGE] «%s»: из %d найденных записей подтверждено %v", original.Artist, len(cands), resp.Same)
	return resp.Same, nil
}
