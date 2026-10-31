// Package proxy реализует прозрачный HTTP Reverse Proxy для Navidrome
// с перехватом поисковых запросов и fan-out объединением результатов.
//
// Данный файл multi_merge_test.go тестирует объединение произвольного количества
// JSON и XML ответов Subsonic от параллельных поисковых запросов.
package proxy

import (
	"encoding/json"
	"testing"
)

func TestMergeMultipleJSONResponses(t *testing.T) {
	resp1 := []byte(`{
		"subsonic-response": {
			"status": "ok",
			"version": "1.16.1",
			"searchResult3": {
				"song": [
					{"id": "song-1", "title": "Cut the Bridge", "artist": "Bad Omens"},
					{"id": "song-2", "title": "The Death of Peace of Mind", "artist": "Bad Omens"}
				]
			}
		}
	}`)

	resp2 := []byte(`{
		"subsonic-response": {
			"status": "ok",
			"version": "1.16.1",
			"searchResult3": {
				"song": [
					{"id": "song-1", "title": "Cut the Bridge", "artist": "Bad Omens"},
					{"id": "song-3", "title": "Under the Bridge", "artist": "Red Hot Chili Peppers"}
				]
			}
		}
	}`)

	resp3 := []byte(`{
		"subsonic-response": {
			"status": "ok",
			"version": "1.16.1",
			"searchResult3": {
				"song": [
					{"id": "song-4", "title": "London Bridge", "artist": "Fergie"}
				]
			}
		}
	}`)

	merged, err := MergeMultipleJSONResponses([][]byte{resp1, resp2, resp3}, 10, 10, 10, "bridge", false)
	if err != nil {
		t.Fatalf("MergeMultipleJSONResponses завершился с ошибкой: %v", err)
	}

	var env SubsonicJSONEnvelope
	if err := json.Unmarshal(merged, &env); err != nil {
		t.Fatalf("Ошибка парсинга итогового JSON: %v", err)
	}

	rawSongs := env.SubsonicResponse["searchResult3"]
	var res SubsonicResult
	_ = json.Unmarshal(rawSongs, &res)

	// Ожидаем 4 уникальных трека (song-1 не должен дублироваться)
	if len(res.Song) != 4 {
		t.Errorf("Ожидалось 4 уникальных трека, получено %d: %+v", len(res.Song), res.Song)
	}
}
