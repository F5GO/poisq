// Package proxy реализует прозрачный HTTP Reverse Proxy для Navidrome
// с перехватом поисковых запросов и fan-out объединением результатов.
//
// Данный файл merge_xml.go реализует объединение и дедупликацию стандартных XML-ответов
// эндпоинтов /rest/search3 и /rest/search2 в протоколе Subsonic.
package proxy

import (
	"encoding/xml"
	"strings"
)

// SubsonicXMLEntity представляет базовый XML-элемент (artist, album, song) с атрибутами и вложенными тегами.
type SubsonicXMLEntity struct {
	XMLName xml.Name
	Attrs   []xml.Attr `xml:",any,attr"`
	Content []byte     `xml:",innerxml"`
}

// SubsonicXMLSearchResult представляет контейнер searchResult3 или searchResult2.
type SubsonicXMLSearchResult struct {
	XMLName  xml.Name
	Entities []SubsonicXMLEntity `xml:",any"`
}

// SubsonicXMLResponse представляет корневой элемент subsonic-response в XML.
type SubsonicXMLResponse struct {
	XMLName       xml.Name                 `xml:"http://subsonic.org/restapi subsonic-response"`
	Status        string                   `xml:"status,attr"`
	Version       string                   `xml:"version,attr"`
	Type          string                   `xml:"type,attr,omitempty"`
	ServerVersion string                   `xml:"serverVersion,attr,omitempty"`
	OpenSubsonic  string                   `xml:"openSubsonic,attr,omitempty"`
	SearchResult3 *SubsonicXMLSearchResult `xml:"searchResult3,omitempty"`
	SearchResult2 *SubsonicXMLSearchResult `xml:"searchResult2,omitempty"`
	SearchResult  *SubsonicXMLSearchResult `xml:"searchResult,omitempty"`
}

func extractID(attrs []xml.Attr) string {
	for _, a := range attrs {
		if a.Name.Local == "id" {
			return a.Value
		}
	}
	return ""
}

func mergeXMLEntities(listA, listB []SubsonicXMLEntity, maxArtists, maxAlbums, maxSongs int, rawQuery string, decorateTitles bool) []SubsonicXMLEntity {
	counts := map[string]int{"artist": 0, "album": 0, "song": 0}
	limits := map[string]int{"artist": maxArtists, "album": maxAlbums, "song": maxSongs}

	var merged []SubsonicXMLEntity
	seenIDs := make(map[string]bool)

	addEntity := func(e SubsonicXMLEntity) {
		name := strings.ToLower(e.XMLName.Local)
		limit, known := limits[name]
		if known && (limit <= 0 || counts[name] >= limit) {
			return
		}

		id := extractID(e.Attrs)
		if id != "" {
			if seenIDs[id] {
				return
			}
			seenIDs[id] = true
		}

		if decorateTitles && strings.TrimSpace(rawQuery) != "" {
			decorateXMLEntity(&e, rawQuery)
		}

		merged = append(merged, e)
		if known {
			counts[name]++
		}
	}

	for _, e := range listA {
		addEntity(e)
	}
	for _, e := range listB {
		addEntity(e)
	}

	return merged
}

func decorateXMLEntity(e *SubsonicXMLEntity, rawQuery string) {
	tag := strings.ToLower(e.XMLName.Local)
	targetAttr := "name"
	if tag == "song" || tag == "entry" || tag == "child" {
		targetAttr = "title"
	}

	cleanQuery := strings.TrimSpace(rawQuery)
	if cleanQuery == "" {
		return
	}

	for i, a := range e.Attrs {
		if strings.EqualFold(a.Name.Local, targetAttr) {
			decorated := DecorateEntityName(a.Value, cleanQuery)
			if decorated != a.Value {
				e.Attrs[i].Value = decorated
			}
			return
		}
	}
}
