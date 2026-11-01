// Package proxy реализует прозрачный HTTP Reverse Proxy для Navidrome
// с перехватом поисковых запросов и fan-out объединением результатов.
//
// Данный файл interceptor_lyrics.go реализует интеллектуальный перехват запросов текстов песен
// Subsonic API (/rest/getLyrics) и OpenSubsonic (/rest/getLyricsBySongId), проверяет кэш,
// обращается к вышестоящему Navidrome и при отсутствии слов задействует каскад провайдеров poisq.
package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/F5GO/poisq/internal/lyrics"
)

// isSubsonicLyricsPath проверяет, является ли путь запросом текстов песен Subsonic / OpenSubsonic.
func isSubsonicLyricsPath(path string) bool {
	return strings.Contains(path, "/rest/getlyricsbysongid") ||
		strings.Contains(path, "/rest/getlyrics")
}

// handleSubsonicLyrics маршрутизирует запрос к соответствующему обработчику текстов.
func (it *Interceptor) handleSubsonicLyrics(w http.ResponseWriter, req *http.Request) {
	if it.lyricsEngine == nil {
		it.reverseProxy.ServeHTTP(w, req)
		return
	}

	path := strings.ToLower(req.URL.Path)
	if strings.Contains(path, "/rest/getlyricsbysongid") {
		it.handleGetLyricsBySongId(w, req)
		return
	}

	it.handleGetLyrics(w, req)
}

func (it *Interceptor) handleGetLyrics(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()
	artist := strings.TrimSpace(q.Get("artist"))
	title := strings.TrimSpace(q.Get("title"))

	if artist == "" || title == "" {
		it.reverseProxy.ServeHTTP(w, req)
		return
	}

	// 1. Безопасность: проверяем аутентификацию через Navidrome
	rec := httptest.NewRecorder()
	it.reverseProxy.ServeHTTP(rec, req)

	body := rec.Body.Bytes()

	// Если Navidrome отклонил аутентификацию (401, 403 или Subsonic status="failed") — немедленно прерываем
	if isSubsonicAuthFailed(rec.Code, body) {
		copyHeaders(w.Header(), rec.Header())
		w.WriteHeader(rec.Code)
		_, _ = w.Write(body)
		return
	}

	// 2. Если у Navidrome уже есть готовые слова (теги или nd-lyrics) — отдаем клиенту
	if rec.Code == http.StatusOK && hasMeaningfulLyrics(body) {
		copyHeaders(w.Header(), rec.Header())
		w.WriteHeader(rec.Code)
		_, _ = w.Write(body)
		return
	}

	// 3. Теперь, когда пользователь гарантированно авторизован, проверяем кэш poisq
	if cached, hit := it.lyricsEngine.GetCache().Get(artist, title); hit && cached != nil {
		lyrics.FormatSubsonicGetLyrics(w, req, artist, title, cached)
		return
	}

	// 4. У Navidrome слов нет — подключаем каскад провайдеров poisq
	searchCtx, cancel := context.WithTimeout(req.Context(), 6*time.Second)
	defer cancel()

	lyr, err := it.lyricsEngine.FindLyrics(searchCtx, lyrics.SearchQuery{
		Artist: artist,
		Title:  title,
	})

	if err == nil && lyr != nil {
		lyrics.FormatSubsonicGetLyrics(w, req, artist, title, lyr)
		return
	}

	// Если и наши провайдеры ничего не нашли — возвращаем исходный ответ Navidrome
	copyHeaders(w.Header(), rec.Header())
	w.WriteHeader(rec.Code)
	_, _ = w.Write(body)
}

func (it *Interceptor) handleGetLyricsBySongId(w http.ResponseWriter, req *http.Request) {
	songID := strings.TrimSpace(req.URL.Query().Get("id"))
	if songID == "" {
		it.reverseProxy.ServeHTTP(w, req)
		return
	}

	// 1. Безопасность: проверяем аутентификацию через Navidrome
	rec := httptest.NewRecorder()
	it.reverseProxy.ServeHTTP(rec, req)

	body := rec.Body.Bytes()

	// Если Navidrome отклонил аутентификацию — немедленно возвращаем отказ
	if isSubsonicAuthFailed(rec.Code, body) {
		copyHeaders(w.Header(), rec.Header())
		w.WriteHeader(rec.Code)
		_, _ = w.Write(body)
		return
	}

	// 2. Если у Navidrome уже есть слова — отдаем
	if rec.Code == http.StatusOK && hasMeaningfulLyrics(body) {
		copyHeaders(w.Header(), rec.Header())
		w.WriteHeader(rec.Code)
		_, _ = w.Write(body)
		return
	}

	// 3. Извлекаем метаданные трека из базы данных Navidrome (если доступна)
	var artist, title, album string
	var durationSec int64
	db := it.lyricsEngine.GetDB()
	if db != nil {
		_ = db.QueryRow(
			"SELECT artist, title, album, duration FROM media_file WHERE id = ? LIMIT 1",
			songID,
		).Scan(&artist, &title, &album, &durationSec)
	}

	// Если нашли в базе и в кэше есть текст — отдаем сразу
	if artist != "" && title != "" {
		if cached, hit := it.lyricsEngine.GetCache().Get(artist, title); hit && cached != nil {
			lyrics.FormatOpenSubsonicLyrics(w, req, cached)
			return
		}

		searchCtx, cancel := context.WithTimeout(req.Context(), 6*time.Second)
		defer cancel()

		lyr, err := it.lyricsEngine.FindLyrics(searchCtx, lyrics.SearchQuery{
			Artist:   artist,
			Title:    title,
			Album:    album,
			Duration: time.Duration(durationSec) * time.Second,
			TrackID:  songID,
		})

		if err == nil && lyr != nil {
			lyrics.FormatOpenSubsonicLyrics(w, req, lyr)
			return
		}
	}

	// Возвращаем исходный ответ Navidrome
	copyHeaders(w.Header(), rec.Header())
	w.WriteHeader(rec.Code)
	_, _ = w.Write(body)
}

// isSubsonicAuthFailed определяет, отклонил ли Navidrome аутентификацию клиента.
func isSubsonicAuthFailed(statusCode int, body []byte) bool {
	if statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden {
		return true
	}
	s := string(body)
	return strings.Contains(s, `status="failed"`) ||
		strings.Contains(s, `"status":"failed"`) ||
		strings.Contains(s, `code="40"`) ||
		strings.Contains(s, `"code":40`) ||
		strings.Contains(s, `code="10"`) ||
		strings.Contains(s, `"code":10`)
}

// hasMeaningfulLyrics проверяет, содержит ли ответ реальный текст песни.
func hasMeaningfulLyrics(body []byte) bool {
	if len(body) == 0 {
		return false
	}

	s := string(body)

	// Пустые теги или пустые поля
	if strings.Contains(s, "<lyrics></lyrics>") ||
		strings.Contains(s, "<lyrics/>") ||
		strings.Contains(s, "\"lyrics\":\"\"") ||
		strings.Contains(s, "\"lyrics\":null") ||
		strings.Contains(s, "<structuredLyrics></structuredLyrics>") ||
		strings.Contains(s, "<lyricsList></lyricsList>") ||
		strings.Contains(s, "<lyricsList/>") {
		return false
	}

	// Наличие признаков текста
	if strings.Contains(s, "<lyrics") && !strings.Contains(s, "<lyrics></lyrics>") {
		// Проверим, что внутри тега есть буквы
		idx := strings.Index(s, "<lyrics")
		end := strings.Index(s, "</lyrics>")
		if idx != -1 && end != -1 && end > idx {
			content := s[idx:end]
			closingTag := strings.Index(content, ">")
			if closingTag != -1 {
				inner := strings.TrimSpace(content[closingTag+1:])
				return len(inner) > 0
			}
		}
	}

	if strings.Contains(s, "\"structuredLyrics\"") && strings.Contains(s, "\"line\"") {
		return true
	}

	return false
}

func copyHeaders(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}
