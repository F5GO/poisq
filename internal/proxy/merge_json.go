// Package proxy реализует прозрачный HTTP Reverse Proxy для Navidrome
// с перехватом поисковых запросов и fan-out объединением результатов.
//
// Данный файл merge_json.go реализует объединение и дедупликацию JSON-ответов
// эндпоинтов Subsonic API с сохранением всех исходных метаданных upstream (OpenSubsonic)
// и дедупликацией артистов, альбомов и треков по идентификаторам.
package proxy

import (
	"encoding/json"
	"strings"
)

// SubsonicJSONEnvelope представляет универсальную обертку ответа Subsonic API без потери исходных полей.
type SubsonicJSONEnvelope struct {
	SubsonicResponse map[string]json.RawMessage `json:"subsonic-response"`
}

// SubsonicResult содержит массивы найденных исполнителей, альбомов и треков.
type SubsonicResult struct {
	Artist []json.RawMessage `json:"artist"`
	Album  []json.RawMessage `json:"album"`
	Song   []json.RawMessage `json:"song"`
}

type entityWithID struct {
	ID string `json:"id"`
}

func mergeEntities(listA, listB []json.RawMessage, maxCount int, targetKey, rawQuery string, decorateTitles bool) []json.RawMessage {
	if maxCount <= 0 {
		return []json.RawMessage{}
	}

	var merged []json.RawMessage
	seenIDs := make(map[string]bool)

	cleanQuery := strings.TrimSpace(rawQuery)

	addEntity := func(raw json.RawMessage) bool {
		if len(merged) >= maxCount {
			return false
		}
		var item entityWithID
		if err := json.Unmarshal(raw, &item); err == nil && item.ID != "" {
			if seenIDs[item.ID] {
				return true
			}
			seenIDs[item.ID] = true
		}

		if decorateTitles && cleanQuery != "" {
			var obj map[string]interface{}
			if err := json.Unmarshal(raw, &obj); err == nil {
				if val, ok := obj[targetKey].(string); ok {
					decorated := DecorateEntityName(val, cleanQuery)
					if decorated != val {
						obj[targetKey] = decorated
						if modifiedRaw, err := json.Marshal(obj); err == nil {
							raw = modifiedRaw
						}
					}
				}
			}
		}

		merged = append(merged, raw)
		return len(merged) < maxCount
	}

	for _, item := range listA {
		if !addEntity(item) {
			return merged
		}
	}

	for _, item := range listB {
		if !addEntity(item) {
			return merged
		}
	}

	return merged
}
