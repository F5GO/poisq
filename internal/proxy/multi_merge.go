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
			if searchKey == "" {
				if _, ok := env.SubsonicResponse["searchResult3"]; ok {
					searchKey = "searchResult3"
				} else if _, ok := env.SubsonicResponse["searchResult2"]; ok {
					searchKey = "searchResult2"
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
	isSearch3 := true

	for _, resp := range responses {
		if len(resp) == 0 {
			continue
		}
		var doc SubsonicXMLResponse
		if err := xml.Unmarshal(resp, &doc); err == nil {
			if doc.SearchResult3 != nil {
				isSearch3 = true
				validDocs = append(validDocs, doc)
			} else if doc.SearchResult2 != nil {
				isSearch3 = false
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
		if isSearch3 && doc.SearchResult3 != nil {
			list = doc.SearchResult3.Entities
		} else if !isSearch3 && doc.SearchResult2 != nil {
			list = doc.SearchResult2.Entities
		}
		allEntities = append(allEntities, list...)
	}

	mergedEntities := mergeXMLEntities(allEntities, nil, maxArtists, maxAlbums, maxSongs, rawQuery, decorateTitles)

	targetTag := xml.Name{Space: "http://subsonic.org/restapi", Local: "searchResult3"}
	if !isSearch3 {
		targetTag.Local = "searchResult2"
	}

	if isSearch3 {
		baseDoc.SearchResult3 = &SubsonicXMLSearchResult{
			XMLName:  targetTag,
			Entities: mergedEntities,
		}
	} else {
		baseDoc.SearchResult2 = &SubsonicXMLSearchResult{
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
