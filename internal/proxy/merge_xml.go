// Package proxy реализует прозрачный HTTP Reverse Proxy для Navidrome
// с перехватом поисковых запросов и fan-out объединением результатов.
//
// Данный файл merge_xml.go реализует объединение и дедупликацию стандартных XML-ответов
// эндпоинтов /rest/search3 и /rest/search2 в протоколе Subsonic.
package proxy

import (
	"bytes"
	"encoding/xml"
	"io"
	"strings"
)

// SubsonicXMLEntity представляет базовый XML-элемент (artist, album, song) с атрибутами.
type SubsonicXMLEntity struct {
	XMLName xml.Name
	Attrs   []xml.Attr `xml:",any,attr"`
	Content []byte     `xml:",innerxml"`
	ID      string     `xml:"-"`
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
}

// MergeXMLSearchResponses объединяет два XML-ответа Subsonic, дедуплицируя сущности по id,
// и при необходимости адаптируя названия для мобильных клиентов со строгой локальной фильтрацией (Arpeggi, Amperfy).
func MergeXMLSearchResponses(respA, respB []byte, maxArtists, maxAlbums, maxSongs int, rawQuery string, decorateTitles bool) ([]byte, error) {
	if len(respA) == 0 && len(respB) == 0 {
		return nil, nil
	}
	if len(respA) == 0 {
		respA, respB = respB, nil
	}

	var dataA, dataB SubsonicXMLResponse
	if err := xml.Unmarshal(respA, &dataA); err != nil {
		return respA, nil
	}
	hasB := false
	if len(respB) > 0 {
		if err := xml.Unmarshal(respB, &dataB); err == nil {
			hasB = true
		}
	}

	isSearch3 := dataA.SearchResult3 != nil || (hasB && dataB.SearchResult3 != nil)
	resA := dataA.SearchResult3
	var resB *SubsonicXMLSearchResult
	if hasB {
		resB = dataB.SearchResult3
	}

	if !isSearch3 {
		resA = dataA.SearchResult2
		if hasB {
			resB = dataB.SearchResult2
		}
	}

	var listA, listB []SubsonicXMLEntity
	if resA != nil {
		listA = resA.Entities
	}
	if resB != nil {
		listB = resB.Entities
	}

	mergedEntities := mergeXMLEntities(listA, listB, maxArtists, maxAlbums, maxSongs, rawQuery, decorateTitles)

	var targetTag xml.Name
	if isSearch3 {
		targetTag = xml.Name{Space: "http://subsonic.org/restapi", Local: "searchResult3"}
		dataA.SearchResult3 = &SubsonicXMLSearchResult{
			XMLName:  targetTag,
			Entities: mergedEntities,
		}
	} else {
		targetTag = xml.Name{Space: "http://subsonic.org/restapi", Local: "searchResult2"}
		dataA.SearchResult2 = &SubsonicXMLSearchResult{
			XMLName:  targetTag,
			Entities: mergedEntities,
		}
	}

	var buf bytes.Buffer
	buf.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	enc := xml.NewEncoder(&buf)
	enc.Indent("", "  ")
	if err := enc.Encode(dataA); err != nil {
		return respA, nil
	}

	return buf.Bytes(), nil
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

// UnmarshalXML кастомно разбирает элементы SubsonicXMLEntity с извлечением ID.
func (e *SubsonicXMLEntity) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	e.XMLName = start.Name
	e.Attrs = start.Attr
	e.ID = extractID(start.Attr)

	for {
		token, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if end, ok := token.(xml.EndElement); ok && end.Name == start.Name {
			break
		}
	}
	return nil
}

// MarshalXML сериализует SubsonicXMLEntity обратно в XML-тег в пространстве имен Subsonic.
func (e SubsonicXMLEntity) MarshalXML(enc *xml.Encoder, start xml.StartElement) error {
	start.Name = xml.Name{Space: "http://subsonic.org/restapi", Local: e.XMLName.Local}
	start.Attr = e.Attrs
	if err := enc.EncodeToken(start); err != nil {
		return err
	}
	if len(e.Content) > 0 {
		if err := enc.EncodeToken(xml.CharData(e.Content)); err != nil {
			return err
		}
	}
	return enc.EncodeToken(start.End())
}
