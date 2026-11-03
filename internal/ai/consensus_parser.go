// Package ai реализует облачные AI-пайплайны сервиса poisq: мультиязычную очистку
// и консенсус текстов через Gemini, сжатие аудио на лету в моно-Opus
// и двухэтапную генерацию синхронизированного караоке через Groq Whisper + Gemini.
//
// Что делает этот код и для чего он нужен:
// Данный файл consensus_parser.go реализует сверхустойчивый к сбоям парсер JSON-ответов
// моделей Gemini (ConsensusOutput). Он автоматически устраняет синтаксические погрешности LLM:
// markdown-блоки, перепутанные квадратные/фигурные скобки, массив строк вместо одиночной строки,
// а также предоставляет аварийный regex-экстрактор LRC-таймкодов при битом JSON.
package ai

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var (
	// lrcLinePattern ищет любые строки караоке с таймкодами [mm:ss.xx] даже внутри поврежденного JSON
	lrcLinePattern = regexp.MustCompile(`(?m)\[\d{2}:\d{2}(?:\.\d{2,3})?\].*$`)
	// langCodePattern ищет код языка в битом JSON
	langCodePattern = regexp.MustCompile(`"langCode"\s*:\s*"([a-zA-Z_-]+)"`)
)

// ParseConsensusOutput надежно разбирает JSON-ответ от Gemini в структуру ConsensusOutput.
func ParseConsensusOutput(raw string) (*ConsensusOutput, error) {
	cleaned := cleanJSONMarkdown(raw)
	if cleaned == "" {
		return nil, fmt.Errorf("пустой ответ от модели")
	}

	// 1. Попытка стандартного разбора через промежуточную гибкую структуру
	out, err := tryFlexibleUnmarshal(cleaned)
	if err == nil && strings.TrimSpace(out.CleanedLyrics) != "" {
		return out, nil
	}

	// 2. Исправление типичных синтаксических ошибок LLM:
	// - Замена ошибочной закрывающей квадратной скобки ']' в конце объекта на '}'
	repaired := repairMalformedJSON(cleaned)
	out, err = tryFlexibleUnmarshal(repaired)
	if err == nil && strings.TrimSpace(out.CleanedLyrics) != "" {
		return out, nil
	}

	// 3. Аварийный фоллбэк: прямое извлечение таймкодов караоке по регулярному выражению
	extracted := extractLRCFromBrokenText(raw)
	if extracted != nil && strings.TrimSpace(extracted.CleanedLyrics) != "" {
		return extracted, nil
	}

	return nil, fmt.Errorf("не удалось извлечь валидный LRC из ответа Gemini (исходная ошибка: %w)", err)
}

// cleanJSONMarkdown удаляет markdown-обертку ```json ... ``` и пробельные символы.
func cleanJSONMarkdown(s string) string {
	trimmed := strings.TrimSpace(s)
	// Удаление открывающего блока ```json или ```
	if strings.HasPrefix(trimmed, "```") {
		idx := strings.Index(trimmed, "\n")
		if idx != -1 {
			trimmed = strings.TrimSpace(trimmed[idx+1:])
		}
	}
	// Удаление закрывающего блока ```
	if strings.HasSuffix(trimmed, "```") {
		trimmed = strings.TrimSpace(strings.TrimSuffix(trimmed, "```"))
	}
	return trimmed
}

// repairMalformedJSON исправляет частые синтаксические огрехи LLM в JSON.
func repairMalformedJSON(s string) string {
	res := strings.TrimSpace(s)

	// Случай: объект начался с '{', а закрылся ']'
	if strings.HasPrefix(res, "{") && strings.HasSuffix(res, "]") {
		res = res[:len(res)-1] + "}"
	}

	// Случай: массив из одного объекта [ { ... } ]
	if strings.HasPrefix(res, "[") && strings.HasSuffix(res, "]") {
		inner := strings.TrimSpace(res[1 : len(res)-1])
		if strings.HasPrefix(inner, "{") && strings.HasSuffix(inner, "}") {
			res = inner
		}
	}

	// Случай: объект закрылся как '}]'
	if strings.HasSuffix(res, "}]") {
		res = strings.TrimSuffix(res, "]")
	}

	return res
}

// tryFlexibleUnmarshal разбирает JSON с поддержкой как строки, так и массива строк в cleanedLyrics.
func tryFlexibleUnmarshal(jsonStr string) (*ConsensusOutput, error) {
	type rawOutput struct {
		LangCode      string          `json:"langCode"`
		IsSynced      *bool           `json:"isSynced"`
		CleanedLyrics json.RawMessage `json:"cleanedLyrics"`
		Lyrics        json.RawMessage `json:"lyrics"`
	}

	var raw rawOutput
	if err := json.Unmarshal([]byte(jsonStr), &raw); err != nil {
		return nil, err
	}

	targetLyrics := raw.CleanedLyrics
	if len(targetLyrics) == 0 {
		targetLyrics = raw.Lyrics
	}

	var lyricsText string
	if len(targetLyrics) > 0 {
		// Попытка 1: строка
		var strVal string
		if err := json.Unmarshal(targetLyrics, &strVal); err == nil {
			lyricsText = strVal
		} else {
			// Попытка 2: массив строк
			var arrVal []string
			if errArr := json.Unmarshal(targetLyrics, &arrVal); errArr == nil {
				lyricsText = strings.Join(arrVal, "\n")
			}
		}
	}

	lyricsText = strings.TrimSpace(lyricsText)
	isSynced := true
	if raw.IsSynced != nil {
		isSynced = *raw.IsSynced
	} else if strings.Contains(lyricsText, "[") && strings.Contains(lyricsText, "]") {
		isSynced = true
	}

	langCode := raw.LangCode
	if langCode == "" {
		langCode = "ru"
	}

	return &ConsensusOutput{
		LangCode:      langCode,
		IsSynced:      isSynced,
		CleanedLyrics: lyricsText,
	}, nil
}

// extractLRCFromBrokenText аварийно извлекает строки [mm:ss.xx] из любого поврежденного текста.
func extractLRCFromBrokenText(raw string) *ConsensusOutput {
	matches := lrcLinePattern.FindAllString(raw, -1)
	if len(matches) == 0 {
		return nil
	}

	var cleanedLines []string
	for _, m := range matches {
		line := strings.TrimSpace(m)
		// Убираем случайные экранирующие кавычки в конце строки JSON
		line = strings.Trim(line, `",\`)
		if line != "" {
			cleanedLines = append(cleanedLines, line)
		}
	}

	if len(cleanedLines) == 0 {
		return nil
	}

	lang := "ru"
	if sub := langCodePattern.FindStringSubmatch(raw); len(sub) > 1 {
		lang = sub[1]
	}

	return &ConsensusOutput{
		LangCode:      lang,
		IsSynced:      true,
		CleanedLyrics: strings.Join(cleanedLines, "\n"),
	}
}
