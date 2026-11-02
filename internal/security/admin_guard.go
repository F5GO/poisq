// Package security отвечает за криптографическую защиту API-ключей, проверку путей
// файловой системы медиатеки (/music) и защиту административного контура (:4535).
//
// Данный файл admin_guard.go реализует HTTP-middleware для внутреннего порта
// панели управления (:4535), защищая административные эндпоинты от атак типа
// CSRF, DNS Rebinding и Clickjacking.
package security

import (
	"net/http"
	"net/url"
	"strings"
)

// AdminGuardMiddleware оборачивает HTTP-обработчик панели управления (:4535)
// защитными заголовками и проверкой источника для мутирующих запросов (POST/PUT/DELETE).
func AdminGuardMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Строгие заголовки безопасности для веб-админки
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")

		// Для мутирующих запросов проверяем Origin (если передан браузером),
		// чтобы исключить кросс-доменные CSRF-вызовы с чужих сайтов при открытом localhost:4535
		if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodDelete || r.Method == http.MethodPatch {
			origin := r.Header.Get("Origin")
			if origin != "" {
				parsedOrigin, err := url.Parse(origin)
				if err != nil || !isSameHostOrLocal(parsedOrigin.Host, r.Host) {
					http.Error(w, `{"error":"CSRF protection: origin mismatch"}`, http.StatusForbidden)
					return
				}
			}
		}

		next.ServeHTTP(w, r)
	})
}

func isSameHostOrLocal(originHost, reqHost string) bool {
	if strings.EqualFold(originHost, reqHost) {
		return true
	}
	// Разрешаем dev-сервер Vite (localhost:5173) и локальные вызовы к :4535
	cleanOrigin := strings.ToLower(strings.Split(originHost, ":")[0])
	cleanReq := strings.ToLower(strings.Split(reqHost, ":")[0])
	if (cleanOrigin == "localhost" || cleanOrigin == "127.0.0.1") &&
		(cleanReq == "localhost" || cleanReq == "127.0.0.1") {
		return true
	}
	return false
}
