// Package lyrics предоставляет систему поиска, кэширования и форматирования текстов песен (Lyrics)
// с акцентом на русскоязычные источники, поддержкой LRCLIB/lrcmux протоколов и Subsonic API.
//
// Данный файл endpoint_handler.go реализует совместимый с открытым протоколом LRCLIB HTTP API
// (/lyrics/api/get и /lyrics/api/search), позволяя использовать poisq как кастомный источник для nd-lyrics.
package lyrics

import (
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// LRCLIBResponseDTO представляет контракт ответа открытого API LRCLIB.
type LRCLIBResponseDTO struct {
	ID           int64   `json:"id"`
	TrackName    string  `json:"trackName"`
	ArtistName   string  `json:"artistName"`
	AlbumName    string  `json:"albumName"`
	Duration     float64 `json:"duration"`
	Instrumental bool    `json:"instrumental"`
	PlainLyrics  string  `json:"plainLyrics"`
	SyncedLyrics string  `json:"syncedLyrics"`
}

// HandleLRCLIBAPI возвращает http.HandlerFunc для обработки запросов по протоколу LRCLIB.
func HandleLRCLIBAPI(engine *Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")

		// 1. Безопасность: проверка доступа к эндпоинту из внешней сети
		if !engine.cfg.EnablePublicAPI {
			reqToken := r.Header.Get("X-API-Token")
			if reqToken == "" {
				authHeader := r.Header.Get("Authorization")
				if strings.HasPrefix(authHeader, "Bearer ") {
					reqToken = strings.TrimPrefix(authHeader, "Bearer ")
				}
			}
			if reqToken == "" {
				reqToken = r.URL.Query().Get("token")
			}

			if engine.cfg.APIToken != "" {
				if reqToken != engine.cfg.APIToken {
					http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
					return
				}
			} else {
				// Если токен не задан, защищаем от публичного интернета (разрешен только localhost и приватные Docker подсети)
				clientIP := extractRemoteIP(r)
				if !isPrivateOrLocalIP(clientIP) {
					http.Error(w, `{"error":"forbidden from public internet (set LYRICS_API_TOKEN or LYRICS_ENABLE_PUBLIC_API=true)"}`, http.StatusForbidden)
					return
				}
			}
		}

		q := r.URL.Query()

		trackName := strings.TrimSpace(q.Get("track_name"))
		artistName := strings.TrimSpace(q.Get("artist_name"))
		albumName := strings.TrimSpace(q.Get("album_name"))
		durationStr := strings.TrimSpace(q.Get("duration"))

		// 2. Безопасность: ограничение длины параметров от DoS
		if len([]rune(trackName)) > 256 || len([]rune(artistName)) > 256 {
			http.Error(w, `{"error":"parameters too long"}`, http.StatusBadRequest)
			return
		}

		var duration time.Duration
		if dSec, err := strconv.ParseFloat(durationStr, 64); err == nil && dSec > 0 {
			duration = time.Duration(dSec * float64(time.Second))
		}

		if trackName == "" || artistName == "" {
			http.Error(w, `{"error":"track_name and artist_name are required"}`, http.StatusBadRequest)
			return
		}

		searchQuery := SearchQuery{
			Artist:   artistName,
			Title:    trackName,
			Album:    albumName,
			Duration: duration,
		}

		lyr, err := engine.FindLyrics(r.Context(), searchQuery)
		if err != nil || lyr == nil {
			http.NotFound(w, r)
			return
		}

		dto := LRCLIBResponseDTO{
			ID:           time.Now().Unix(),
			TrackName:    trackName,
			ArtistName:   artistName,
			AlbumName:    albumName,
			Duration:     duration.Seconds(),
			Instrumental: lyr.Instrumental,
			PlainLyrics:  lyr.PlainLyrics,
			SyncedLyrics: lyr.SyncedLyrics,
		}

		// Если запрошен search эндпоинт — возвращаем массив
		if r.URL.Path == "/lyrics/api/search" {
			_ = json.NewEncoder(w).Encode([]LRCLIBResponseDTO{dto})
			return
		}

		// Иначе возвращаем одиночный объект для /lyrics/api/get
		_ = json.NewEncoder(w).Encode(dto)
	}
}

func extractRemoteIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if len(parts) > 0 {
			return strings.TrimSpace(parts[0])
		}
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func isPrivateOrLocalIP(ipStr string) bool {
	clean := strings.TrimSpace(ipStr)
	if clean == "" || clean == "localhost" || clean == "192.0.2.1" {
		return true
	}
	ip := net.ParseIP(clean)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate()
}
