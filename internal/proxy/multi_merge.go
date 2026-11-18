// Package proxy реализует прозрачный HTTP Reverse Proxy для Navidrome
// с перехватом поисковых запросов и fan-out объединением результатов.
//
// Данный файл multi_merge.go реализует параллельное N-стороннее объединение (Multi-Merge)
// результатов поиска Subsonic API (JSON и XML) от нескольких взаимодополняющих запросов
// с дедупликацией сущностей по ID и адаптацией названий для мобильных клиентов.
package proxy

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"strings"
)

// MergeMultipleJSONResponses объединяет произвольное количество JSON-ответов Subsonic
// в единый сбалансированный ответ, исключая дубликаты треков, альбомов и исполнителей.
func MergeMultipleJSONResponses(responses [][]byte, maxArtists, maxAlbums, maxSongs int, rawQuery string, decorateTitles bool) ([]byte, error) {
	var validEnvelopes []SubsonicJSONEnvelope
	var searchKey string

	for _, resp := range responses {
		if len(resp) == 0 {
			continue
		}
		var env SubsonicJSONEnvelope
		if err := json.Unmarshal(resp, &env); err == nil && env.SubsonicResponse != nil {
			var status string
			if rawStatus, ok := env.SubsonicResponse["status"]; ok {
				_ = json.Unmarshal(rawStatus, &status)
			}
			if !strings.EqualFold(status, "ok") {
				continue
			}
			if searchKey == "" {
				if _, ok := env.SubsonicResponse["searchResult3"]; ok {
					searchKey = "searchResult3"
				} else if _, ok := env.SubsonicResponse["searchResult2"]; ok {
					searchKey = "searchResult2"
				} else if _, ok := env.SubsonicResponse["searchResult"]; ok {
					searchKey = "searchResult"
				}
			}
			validEnvelopes = append(validEnvelopes, env)
		}
	}

	if len(validEnvelopes) == 0 {
		return nil, nil
	}
	if searchKey == "" {
		searchKey = "searchResult3"
	}

	baseEnvelope := validEnvelopes[0]

	// Собираем сущности со всех ответов
	var allArtists, allAlbums, allSongs []json.RawMessage
	for _, env := range validEnvelopes {
		if raw, ok := env.SubsonicResponse[searchKey]; ok {
			var res SubsonicResult
			if err := json.Unmarshal(raw, &res); err == nil {
				allArtists = append(allArtists, res.Artist...)
				allAlbums = append(allAlbums, res.Album...)
				allSongs = append(allSongs, res.Song...)
			}
		}
	}

	mergedResult := SubsonicResult{
		Artist: mergeEntities(allArtists, nil, maxArtists, "name", rawQuery, decorateTitles),
		Album:  mergeEntities(allAlbums, nil, maxAlbums, "name", rawQuery, decorateTitles),
		Song:   mergeEntities(allSongs, nil, maxSongs, "title", rawQuery, decorateTitles),
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
		return json.Marshal(baseEnvelope)
	}

	baseEnvelope.SubsonicResponse[searchKey] = mergedBytes
	return json.Marshal(baseEnvelope)
}

// MergeMultipleXMLResponses объединяет произвольное количество XML-ответов Subsonic
// в единый непротиворечивый ответ с дедупликацией сущностей по идентификаторам.
func MergeMultipleXMLResponses(responses [][]byte, maxArtists, maxAlbums, maxSongs int, rawQuery string, decorateTitles bool) ([]byte, error) {
	var validDocs []SubsonicXMLResponse
	searchKind := "searchResult3"

	for _, resp := range responses {
		if len(resp) == 0 {
			continue
		}
		var doc SubsonicXMLResponse
		if err := xml.Unmarshal(resp, &doc); err == nil && strings.EqualFold(doc.Status, "ok") {
			if doc.SearchResult3 != nil {
				searchKind = "searchResult3"
				validDocs = append(validDocs, doc)
			} else if doc.SearchResult2 != nil {
				searchKind = "searchResult2"
				validDocs = append(validDocs, doc)
			} else if doc.SearchResult != nil {
				searchKind = "searchResult"
				validDocs = append(validDocs, doc)
			}
		}
	}

	if len(validDocs) == 0 {
		return nil, nil
	}

	baseDoc := validDocs[0]
	var allEntities []SubsonicXMLEntity

	for _, doc := range validDocs {
		var list []SubsonicXMLEntity
		switch searchKind {
		case "searchResult3":
			if doc.SearchResult3 != nil {
				list = doc.SearchResult3.Entities
			}
		case "searchResult2":
			if doc.SearchResult2 != nil {
				list = doc.SearchResult2.Entities
			}
		case "searchResult":
			if doc.SearchResult != nil {
				list = doc.SearchResult.Entities
			}
		}
		allEntities = append(allEntities, list...)
	}

	mergedEntities := mergeXMLEntities(allEntities, nil, maxArtists, maxAlbums, maxSongs, rawQuery, decorateTitles)

	targetTag := xml.Name{Space: "http://subsonic.org/restapi", Local: searchKind}
	switch searchKind {
	case "searchResult3":
		baseDoc.SearchResult3 = &SubsonicXMLSearchResult{
			XMLName:  targetTag,
			Entities: mergedEntities,
		}
	case "searchResult2":
		baseDoc.SearchResult2 = &SubsonicXMLSearchResult{
			XMLName:  targetTag,
			Entities: mergedEntities,
		}
	case "searchResult":
		baseDoc.SearchResult = &SubsonicXMLSearchResult{
			XMLName:  targetTag,
			Entities: mergedEntities,
		}
	}

	var buf bytes.Buffer
	buf.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	enc := xml.NewEncoder(&buf)
	enc.Indent("", "  ")
	if err := enc.Encode(baseDoc); err != nil {
		return responses[0], nil
	}

	return buf.Bytes(), nil
}
