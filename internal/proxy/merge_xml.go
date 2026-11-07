// Package proxy реализует прозрачный HTTP Reverse Proxy для Navidrome
// с перехватом поисковых запросов и fan-out объединением результатов.
//
// Данный файл merge_xml.go реализует объединение и дедупликацию стандартных XML-ответов
// эндпоинтов /rest/search3 и /rest/search2 в протоколе Subsonic.
package proxy

import (
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
