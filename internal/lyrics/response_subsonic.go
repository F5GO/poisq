// Package lyrics предоставляет систему поиска, кэширования и форматирования текстов песен (Lyrics)
// с акцентом на русскоязычные источники, поддержкой LRCLIB/lrcmux протоколов и Subsonic API.
//
// Данный файл response_subsonic.go реализует формирование ответов для эндпоинта /rest/getLyrics
// протокола Subsonic API как в формате XML, так и в формате JSON.
package lyrics

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"
)

// SubsonicResponseXML описывает стандартный XML-ответ Subsonic API для getLyrics.
type SubsonicResponseXML struct {
	XMLName       xml.Name         `xml:"http://subsonic.org/restapi subsonic-response"`
	Status        string           `xml:"status,attr"`
	Version       string           `xml:"version,attr"`
	Type          string           `xml:"type,attr,omitempty"`
	ServerVersion string           `xml:"serverVersion,attr,omitempty"`
	Lyrics        *SubsonicItemXML `xml:"lyrics,omitempty"`
}

// SubsonicItemXML представляет тег <lyrics> в Subsonic API.
type SubsonicItemXML struct {
	Artist string `xml:"artist,attr,omitempty"`
	Title  string `xml:"title,attr,omitempty"`
	Text   string `xml:",chardata"`
}

// FormatSubsonicGetLyrics формирует ответ для /rest/getLyrics в формате XML или JSON в зависимости от параметра format/f.
func FormatSubsonicGetLyrics(w http.ResponseWriter, r *http.Request, artist, title string, lyr *Lyrics) {
	isJSON := strings.ToLower(r.URL.Query().Get("f")) == "json" ||
		strings.ToLower(r.URL.Query().Get("format")) == "json"

	// Приоритет отдаем синхронизированному тексту, если есть
	text := ""
	if lyr != nil {
		if lyr.SyncedLyrics != "" {
			text = lyr.SyncedLyrics
		} else {
			text = lyr.PlainLyrics
		}
	}

	if isJSON {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		resp := map[string]any{
			"subsonic-response": map[string]any{
				"status":        "ok",
				"version":       "1.16.1",
				"type":          "poisq",
				"serverVersion": "0.0.1",
			},
		}

		if text != "" {
			subResp := resp["subsonic-response"].(map[string]any)
			subResp["lyrics"] = map[string]any{
				"artist": artist,
				"title":  title,
				"value":  text,
			}
		}

		_ = json.NewEncoder(w).Encode(resp)
		return
	}

	// Отдача XML
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, xml.Header)

	respXML := SubsonicResponseXML{
		Status:        "ok",
		Version:       "1.16.1",
		Type:          "poisq",
		ServerVersion: "0.0.1",
	}

	if text != "" {
		respXML.Lyrics = &SubsonicItemXML{
			Artist: artist,
			Title:  title,
			Text:   text,
		}
	}

	_ = xml.NewEncoder(w).Encode(respXML)
}
