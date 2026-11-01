// Package proxy реализует прозрачный HTTP Reverse Proxy для Navidrome
// с перехватом поисковых запросов и fan-out объединением результатов.
//
// Данный файл interceptor_lyrics.go реализует интеллектуальный перехват запросов текстов песен
// Subsonic API (/rest/getLyrics) и OpenSubsonic (/rest/getLyricsBySongId), проверяет кэш,
// обращается к вышестоящему Navidrome и при отсутствии слов задействует каскад провайдеров poisq.
package proxy

import (
	"context"
	"errors"
	"io"
	"log"
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
	start := time.Now()
	q := req.URL.Query()
	artist := strings.TrimSpace(q.Get("artist"))
	title := strings.TrimSpace(q.Get("title"))
	clientIP := ExtractClientIP(req)

	if artist == "" || title == "" {
		it.reverseProxy.ServeHTTP(w, req)
		return
	}

	forceRefresh := q.Get("nocache") == "1" || q.Get("refresh") == "1"
	if forceRefresh {
		_ = it.lyricsEngine.GetCache().Delete(artist, title)
		log.Printf("[INFO] [LYRICS] Принудительное обновление кэша для %q - %q", artist, title)
	}

	log.Printf("[LYRICS] Запрос getLyrics: artist=%q, title=%q (IP: %s, Agent: %s)",
		artist, title, clientIP, req.UserAgent())

	// 1. Безопасность: проверяем аутентификацию и наличие встроенных слов через Navidrome
	upstreamRes, err := it.queryNavidromeUpstream(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			log.Printf("[DEBUG] [LYRICS] Navidrome не вернул быстрый ответ за 1с (переходим к источникам poisq)")
		} else {
			log.Printf("[DEBUG] [LYRICS] Статус обращения к Navidrome: %v", err)
		}
	}

	if upstreamRes != nil {
		// Если Navidrome отклонил аутентификацию (401, 403 или Subsonic status="failed") — немедленно прерываем
		if upstreamRes.authFailed {
			log.Printf("[WARN] [LYRICS] Отказ аутентификации от Navidrome для artist=%q, title=%q (status=%d)",
				artist, title, upstreamRes.statusCode)
			copyHeaders(w.Header(), upstreamRes.header)
			w.WriteHeader(upstreamRes.statusCode)
			_, _ = w.Write(upstreamRes.body)
			return
		}

		// 2. Если у Navidrome уже есть готовые слова (теги или nd-lyrics) — отдаем клиенту
		if upstreamRes.hasLyrics {
			log.Printf("[INFO] [LYRICS] Navidrome уже имеет текст для %q - %q (отдаем клиенту за %.1fms)",
				artist, title, float64(time.Since(start).Microseconds())/1000.0)
			copyHeaders(w.Header(), upstreamRes.header)
			w.WriteHeader(upstreamRes.statusCode)
			_, _ = w.Write(upstreamRes.body)
			return
		}
	}

	// 3. Теперь, когда пользователь гарантированно авторизован, проверяем оперативный кэш poisq
	if !forceRefresh {
		if cached, hit := it.lyricsEngine.GetCache().Get(artist, title); hit && cached != nil {
			log.Printf("[INFO] [LYRICS] Cache HIT для %q - %q (%s, %.2fms)",
				artist, title, cached.Kind, float64(time.Since(start).Microseconds())/1000.0)
			lyrics.FormatSubsonicGetLyrics(w, req, artist, title, cached)
			return
		}
	}

	// 4. У Navidrome слов нет — подключаем параллельный поиск в источниках poisq.
	// Фоновый поиск запускается в detached-контексте (10с), чтобы при кратковременном разрыве сокета клиентом
	// слова все равно были найдены, сохранены в SQLite-кэш и мгновенно отданы при повторе запроса.
	type searchRes struct {
		lyr *lyrics.Lyrics
		err error
	}
	resCh := make(chan searchRes, 1)

	go func() {
		bgCtx, bgCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer bgCancel()

		lyr, err := it.lyricsEngine.FindLyrics(bgCtx, lyrics.SearchQuery{
			Artist: artist,
			Title:  title,
		})
		resCh <- searchRes{lyr: lyr, err: err}
	}()

	select {
	case res := <-resCh:
		elapsedMs := float64(time.Since(start).Microseconds()) / 1000.0
		if res.err == nil && res.lyr != nil {
			log.Printf("[INFO] [LYRICS] Успешно найден %s текст для %q - %q из %s [%.1fms]",
				res.lyr.Kind, artist, title, res.lyr.Source, elapsedMs)
			lyrics.FormatSubsonicGetLyrics(w, req, artist, title, res.lyr)
			return
		}
		log.Printf("[INFO] [LYRICS] Текст для %q - %q не найден ни в одном источнике [%.1fms]", artist, title, elapsedMs)

	case <-req.Context().Done():
		log.Printf("[DEBUG] [LYRICS] Клиент отключился до завершения поиска %q - %q (поиск продолжается в фоне)", artist, title)
		return

	case <-time.After(7 * time.Second):
		log.Printf("[WARN] [LYRICS] Превышен таймаут ответа клиенту (7с) для %q - %q (поиск продолжается в фоне)", artist, title)
	}

	// Если наши провайдеры ничего не нашли или таймаут — возвращаем исходный ответ Navidrome
	if upstreamRes != nil {
		copyHeaders(w.Header(), upstreamRes.header)
		w.WriteHeader(upstreamRes.statusCode)
		_, _ = w.Write(upstreamRes.body)
		return
	}

	it.reverseProxy.ServeHTTP(w, req)
}

func (it *Interceptor) handleGetLyricsBySongId(w http.ResponseWriter, req *http.Request) {
	start := time.Now()
	songID := strings.TrimSpace(req.URL.Query().Get("id"))
	clientIP := ExtractClientIP(req)
	forceRefresh := req.URL.Query().Get("nocache") == "1" || req.URL.Query().Get("refresh") == "1"

	if songID == "" {
		it.reverseProxy.ServeHTTP(w, req)
		return
	}

	log.Printf("[LYRICS] Запрос getLyricsBySongId: id=%q (IP: %s, Agent: %s)", songID, clientIP, req.UserAgent())

	// 1. Безопасность: проверяем аутентификацию через Navidrome
	upstreamRes, err := it.queryNavidromeUpstream(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			log.Printf("[DEBUG] [LYRICS] Navidrome не вернул быстрый ответ за 1с (переходим к источникам poisq)")
		} else {
			log.Printf("[DEBUG] [LYRICS] Статус обращения к Navidrome: %v", err)
		}
	}

	if upstreamRes != nil {
		if upstreamRes.authFailed {
			log.Printf("[WARN] [LYRICS] Отказ аутентификации от Navidrome для id=%q", songID)
			copyHeaders(w.Header(), upstreamRes.header)
			w.WriteHeader(upstreamRes.statusCode)
			_, _ = w.Write(upstreamRes.body)
			return
		}

		if upstreamRes.hasLyrics {
			log.Printf("[INFO] [LYRICS] Navidrome уже имеет текст для id=%q (отдаем клиенту за %.1fms)",
				songID, float64(time.Since(start).Microseconds())/1000.0)
			copyHeaders(w.Header(), upstreamRes.header)
			w.WriteHeader(upstreamRes.statusCode)
			_, _ = w.Write(upstreamRes.body)
			return
		}
	}

	// 2. Извлекаем метаданные трека из базы данных Navidrome (если доступна)
	var artist, title, album string
	var durationSec int64
	db := it.lyricsEngine.GetDB()
	if db != nil {
		_ = db.QueryRow(
			"SELECT artist, title, album, duration FROM media_file WHERE id = ? LIMIT 1",
			songID,
		).Scan(&artist, &title, &album, &durationSec)
	}

	if artist != "" && title != "" {
		if forceRefresh {
			_ = it.lyricsEngine.GetCache().Delete(artist, title)
			log.Printf("[INFO] [LYRICS] Принудительное обновление кэша для id=%q (%q - %q)", songID, artist, title)
		} else {
			// Проверяем оперативный кэш
			if cached, hit := it.lyricsEngine.GetCache().Get(artist, title); hit && cached != nil {
				log.Printf("[INFO] [LYRICS] Cache HIT для id=%q (%q - %q, %s, %.2fms)",
					songID, artist, title, cached.Kind, float64(time.Since(start).Microseconds())/1000.0)
				lyrics.FormatOpenSubsonicLyrics(w, req, cached)
				return
			}
		}

		// Запускаем поиск в фоне с защитой от обрыва соединения клиента
		type searchRes struct {
			lyr *lyrics.Lyrics
			err error
		}
		resCh := make(chan searchRes, 1)

		go func() {
			bgCtx, bgCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer bgCancel()

			lyr, err := it.lyricsEngine.FindLyrics(bgCtx, lyrics.SearchQuery{
				Artist:   artist,
				Title:    title,
				Album:    album,
				Duration: time.Duration(durationSec) * time.Second,
				TrackID:  songID,
			})
			resCh <- searchRes{lyr: lyr, err: err}
		}()

		select {
		case res := <-resCh:
			elapsedMs := float64(time.Since(start).Microseconds()) / 1000.0
			if res.err == nil && res.lyr != nil {
				log.Printf("[INFO] [LYRICS] Успешно найден %s текст для id=%q (%q - %q) из %s [%.1fms]",
					res.lyr.Kind, songID, artist, title, res.lyr.Source, elapsedMs)
				lyrics.FormatOpenSubsonicLyrics(w, req, res.lyr)
				return
			}
			log.Printf("[INFO] [LYRICS] Текст для id=%q (%q - %q) не найден ни в одном источнике [%.1fms]",
				songID, artist, title, elapsedMs)

		case <-req.Context().Done():
			log.Printf("[DEBUG] [LYRICS] Клиент отключился до завершения поиска id=%q (%q - %q)", songID, artist, title)
			return

		case <-time.After(7 * time.Second):
			log.Printf("[WARN] [LYRICS] Превышен таймаут ответа клиенту (7с) для id=%q", songID)
		}
	}

	// Возвращаем исходный ответ Navidrome
	if upstreamRes != nil {
		copyHeaders(w.Header(), upstreamRes.header)
		w.WriteHeader(upstreamRes.statusCode)
		_, _ = w.Write(upstreamRes.body)
		return
	}

	it.reverseProxy.ServeHTTP(w, req)
}

type navidromeCheckResult struct {
	statusCode int
	header     http.Header
	body       []byte
	authFailed bool
	hasLyrics  bool
}

// queryNavidromeUpstream отправляет чистый запрос к вышестоящему Navidrome без модификации входящего req.
func (it *Interceptor) queryNavidromeUpstream(req *http.Request) (*navidromeCheckResult, error) {
	if it.upstreamClient != nil && it.targetURL != nil {
		targetURL := *it.targetURL
		targetURL.Path = singleJoiningSlash(it.targetURL.Path, req.URL.Path)
		targetURL.RawQuery = req.URL.RawQuery

		ctx, cancel := context.WithTimeout(req.Context(), 1*time.Second)
		defer cancel()

		upReq, err := http.NewRequestWithContext(ctx, req.Method, targetURL.String(), nil)
		if err != nil {
			return nil, err
		}

		for k, vv := range req.Header {
			if strings.EqualFold(k, "Host") || strings.EqualFold(k, "Content-Length") {
				continue
			}
			for _, v := range vv {
				upReq.Header.Add(k, v)
			}
		}
		upReq.Host = ExtractClientHost(req)

		resp, err := it.upstreamClient.Do(upReq)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
		if err != nil {
			return nil, err
		}

		return &navidromeCheckResult{
			statusCode: resp.StatusCode,
			header:     resp.Header,
			body:       body,
			authFailed: isSubsonicAuthFailed(resp.StatusCode, body),
			hasLyrics:  resp.StatusCode == http.StatusOK && hasMeaningfulLyrics(body),
		}, nil
	}

	// Запасной fallback через httptest.ResponseRecorder (для изолированных тестов без upstreamClient)
	rec := httptest.NewRecorder()
	it.reverseProxy.ServeHTTP(rec, req)
	body := rec.Body.Bytes()

	return &navidromeCheckResult{
		statusCode: rec.Code,
		header:     rec.Header(),
		body:       body,
		authFailed: isSubsonicAuthFailed(rec.Code, body),
		hasLyrics:  rec.Code == http.StatusOK && hasMeaningfulLyrics(body),
	}, nil
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

	// Наличие признаков текста в XML
	if strings.Contains(s, "<lyrics") && !strings.Contains(s, "<lyrics></lyrics>") {
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

	// Наличие структурированных строк в OpenSubsonic JSON
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
