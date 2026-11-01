// Package lyrics предоставляет систему поиска, кэширования и форматирования текстов песен (Lyrics)
// с акцентом на русскоязычные источники, поддержкой LRCLIB/lrcmux протоколов и Subsonic API.
//
// Данный файл response_opensubsonic.go реализует формирование ответов для эндпоинта /rest/getLyricsBySongId
// протокола OpenSubsonic (структурированные синхронизированные строки structuredLyrics) в XML и JSON.
package lyrics

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"
)

// OpenSubsonicResponseXML описывает ответ getLyricsBySongId в формате XML.
type OpenSubsonicResponseXML struct {
	XMLName       xml.Name               `xml:"http://subsonic.org/restapi subsonic-response"`
	Status        string                 `xml:"status,attr"`
	Version       string                 `xml:"version,attr"`
	Type          string                 `xml:"type,attr,omitempty"`
	ServerVersion string                 `xml:"serverVersion,attr,omitempty"`
	LyricsList    *OpenSubsonicLyricsList `xml:"lyricsList,omitempty"`
}

// OpenSubsonicLyricsList содержит список доступных структурированных текстов.
type OpenSubsonicLyricsList struct {
	StructuredLyrics []OpenSubsonicStructured `xml:"structuredLyrics"`
}

// OpenSubsonicStructured описывает вариант текста песни (синхронизированный или plain).
type OpenSubsonicStructured struct {
	Lang   string               `xml:"lang,attr,omitempty"`
	Synced bool                 `xml:"synced,attr"`
	Line   []OpenSubsonicLineXML `xml:"line,omitempty"`
}

// OpenSubsonicLineXML описывает отдельную строку с миллисекундами start.
type OpenSubsonicLineXML struct {
	Start int64  `xml:"start,attr,omitempty"`
	Value string `xml:",chardata"`
}

// FormatOpenSubsonicLyrics формирует ответ для /rest/getLyricsBySongId в формате OpenSubsonic XML или JSON.
func FormatOpenSubsonicLyrics(w http.ResponseWriter, r *http.Request, lyr *Lyrics) {
	isJSON := strings.ToLower(r.URL.Query().Get("f")) == "json" ||
		strings.ToLower(r.URL.Query().Get("format")) == "json"

	if lyr == nil {
		if isJSON {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"subsonic-response": map[string]any{
					"status":        "ok",
					"version":       "1.16.1",
					"type":          "poisq",
					"serverVersion": "0.0.1",
					"lyricsList":    map[string]any{},
				},
			})
			return
		}

		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		_, _ = fmt.Fprint(w, xml.Header)
		_ = xml.NewEncoder(w).Encode(OpenSubsonicResponseXML{
			Status:        "ok",
			Version:       "1.16.1",
			Type:          "poisq",
			ServerVersion: "0.0.1",
			LyricsList:    &OpenSubsonicLyricsList{},
		})
		return
	}

	// Подготавливаем структурированные строки
	isSynced := lyr.Kind == KindLRC && len(lyr.Lines) > 0

	if isJSON {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		var jsonLines []map[string]any

		if isSynced {
			for _, l := range lyr.Lines {
				jsonLines = append(jsonLines, map[string]any{
					"start": l.StartMs,
					"value": l.Text,
				})
			}
		} else {
			// Если plain текст — разбиваем по переносам строк
			for _, l := range strings.Split(lyr.PlainLyrics, "\n") {
				t := strings.TrimSpace(l)
				if t != "" {
					jsonLines = append(jsonLines, map[string]any{
						"value": t,
					})
				}
			}
		}

		resp := map[string]any{
			"subsonic-response": map[string]any{
				"status":        "ok",
				"version":       "1.16.1",
				"type":          "poisq",
				"serverVersion": "0.0.1",
				"lyricsList": map[string]any{
					"structuredLyrics": []map[string]any{
						{
							"lang":   "rus",
							"synced": isSynced,
							"line":   jsonLines,
						},
					},
				},
			},
		}

		_ = json.NewEncoder(w).Encode(resp)
		return
	}

	// XML генерация
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	_, _ = fmt.Fprint(w, xml.Header)

	var xmlLines []OpenSubsonicLineXML
	if isSynced {
		for _, l := range lyr.Lines {
			xmlLines = append(xmlLines, OpenSubsonicLineXML{
				Start: l.StartMs,
				Value: l.Text,
			})
		}
	} else {
		for _, l := range strings.Split(lyr.PlainLyrics, "\n") {
			t := strings.TrimSpace(l)
			if t != "" {
				xmlLines = append(xmlLines, OpenSubsonicLineXML{
					Value: t,
				})
			}
		}
	}

	respXML := OpenSubsonicResponseXML{
		Status:        "ok",
		Version:       "1.16.1",
		Type:          "poisq",
		ServerVersion: "0.0.1",
		LyricsList: &OpenSubsonicLyricsList{
			StructuredLyrics: []OpenSubsonicStructured{
				{
					Lang:   "rus",
					Synced: isSynced,
					Line:   xmlLines,
				},
			},
		},
	}

	_ = xml.NewEncoder(w).Encode(respXML)
}
