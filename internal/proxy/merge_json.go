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

// SubsonicJSONResponse - псевдоним типа для обратной совместимости.
type SubsonicJSONResponse = SubsonicJSONEnvelope

// SubsonicResult содержит массивы найденных исполнителей, альбомов и треков.
type SubsonicResult struct {
	Artist []json.RawMessage `json:"artist"`
	Album  []json.RawMessage `json:"album"`
	Song   []json.RawMessage `json:"song"`
}

type entityWithID struct {
	ID string `json:"id"`
}

// MergeJSONSearchResponses объединяет два JSON-ответа Subsonic, дедуплицируя сущности по их id,
// и при необходимости адаптируя названия для мобильных клиентов со строгой локальной фильтрацией (Arpeggi, Amperfy).
func MergeJSONSearchResponses(respA, respB []byte, maxArtists, maxAlbums, maxSongs int, rawQuery string, decorateTitles bool) ([]byte, error) {
	if len(respA) == 0 && len(respB) == 0 {
		return nil, nil
	}
	if len(respA) == 0 {
		respA, respB = respB, nil
	}

	var dataA, dataB SubsonicJSONEnvelope
	if err := json.Unmarshal(respA, &dataA); err != nil {
		return respA, nil
	}
	hasB := false
	if len(respB) > 0 {
		if err := json.Unmarshal(respB, &dataB); err == nil && dataB.SubsonicResponse != nil {
			hasB = true
		}
	}

	if dataA.SubsonicResponse == nil {
		if hasB {
			return respB, nil
		}
		return respA, nil
	}

	searchKey := "searchResult3"
	rawA, hasSearchA := dataA.SubsonicResponse["searchResult3"]
	var rawB json.RawMessage
	var hasSearchB bool
	if hasB {
		rawB, hasSearchB = dataB.SubsonicResponse["searchResult3"]
	}

	if !hasSearchA && !hasSearchB {
		searchKey = "searchResult2"
		rawA, hasSearchA = dataA.SubsonicResponse["searchResult2"]
		if hasB {
			rawB, hasSearchB = dataB.SubsonicResponse["searchResult2"]
		}
	}

	if !hasSearchA && !hasSearchB {
		return respA, nil
	}

	var resA, resB SubsonicResult
	if hasSearchA {
		_ = json.Unmarshal(rawA, &resA)
	}
	if hasSearchB {
		_ = json.Unmarshal(rawB, &resB)
	}

	mergedResult := SubsonicResult{
		Artist: mergeEntities(resA.Artist, resB.Artist, maxArtists, "name", rawQuery, decorateTitles),
		Album:  mergeEntities(resA.Album, resB.Album, maxAlbums, "name", rawQuery, decorateTitles),
		Song:   mergeEntities(resA.Song, resB.Song, maxSongs, "title", rawQuery, decorateTitles),
	}

	if mergedResult.Artist == nil {
		mergedResult.Artist = []json.RawMessage{}
	}
	if mergedResult.Album == nil {
		mergedResult.Album = []json.RawMessage{}
	}
	if mergedResult.Song == nil {
		mergedResult.Song = []json.RawMessage{}
	}

	mergedBytes, err := json.Marshal(mergedResult)
	if err != nil {
		return respA, nil
	}

	dataA.SubsonicResponse[searchKey] = mergedBytes
	return json.Marshal(dataA)
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
