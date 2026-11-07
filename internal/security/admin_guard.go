// Package security отвечает за криптографическую защиту API-ключей, проверку путей
// файловой системы медиатеки (/music) и защиту административного контура (:4535).
//
// Данный файл admin_guard.go реализует HTTP-middleware для внутреннего порта
// панели управления (:4535), защищая административные эндпоинты от атак типа
// CSRF, DNS Rebinding и Clickjacking.
package security

import (
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// AdminGuardMiddleware оборачивает HTTP-обработчик панели управления (:4535)
// защитными заголовками и проверкой источника для мутирующих запросов (POST/PUT/DELETE).
func AdminGuardMiddleware(next http.Handler) http.Handler {
	extra := strings.Split(os.Getenv("ADMIN_HOSTS"), ",")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// DNS rebinding: чужой домен, переключённый на 127.0.0.1, приходит со своим же Host и Origin, поэтому сверки Origin с Host мало.
		// Пускаем только адреса, которые нельзя получить через публичный DNS; остальные имена — через ADMIN_HOSTS.
		if !isAdminHost(r.Host, extra) {
			http.Error(w, `{"error":"host not allowed: add it to ADMIN_HOSTS"}`, http.StatusForbidden)
			return
		}
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

// isAdminHost: IP-адрес, имя без точки или локальная зона — их не выдаст публичный DNS; плюс имена из ADMIN_HOSTS.
func isAdminHost(hostport string, extra []string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.ToLower(strings.Trim(host, "[]"))
	if host == "" {
		return false
	}
	if net.ParseIP(host) != nil || !strings.Contains(host, ".") {
		return true
	}
	for _, zone := range []string{".localhost", ".local", ".lan", ".internal", ".home.arpa"} {
		if strings.HasSuffix(host, zone) {
			return true
		}
	}
	for _, e := range extra {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" && e == host {
			return true
		}
	}
	return false
}
