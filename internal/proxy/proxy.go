// Package proxy реализует прозрачный HTTP Reverse Proxy для Navidrome
// с перехватом поисковых запросов и fan-out объединением результатов.
//
// Данный файл proxy.go конфигурирует защищенный HTTP Reverse Proxy, изоляцию
// диагностических эндпоинтов за флагом EnableDebugAPI, корректную передачу X-Forwarded-* заголовков и поддержку CDN (Cloudflare/Nginx).
package proxy

import (
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/F5GO/poisq/internal/cache"
	"github.com/F5GO/poisq/internal/cast"
	"github.com/F5GO/poisq/internal/search"
)

// Server инкапсулирует HTTP-обработчик реверс-прокси и вспомогательные сервисы.
type Server struct {
	handler     http.Handler
	cast        http.Handler // трансляция на колонки (poisqCast*); nil — выключена
	biographies effectiveBiographyReader
}

// SetCast подключает эндпоинты трансляции. Вызывать до запуска HTTP-сервера.
func (s *Server) SetCast(h http.Handler) { s.cast = h }

// SetBiographyReader подключает локальную выдачу проверенных биографий.
// Вызывать до запуска HTTP-сервера.
func (s *Server) SetBiographyReader(reader effectiveBiographyReader) { s.biographies = reader }

// NewServer создает новый экземпляр реверс-прокси сервера с оптимизированным пулом соединений и кэшем.
func NewServer(targetURL *url.URL, engine *search.Engine, dict *search.Dictionary, aliasMgr *search.AliasManager, enableDebugAPI bool) *Server {
	srv := &Server{}
	// Высокопроизводительный пул Keep-Alive соединений с Navidrome для мгновенной отдачи ответов
	transport := &http.Transport{
		Proxy: nil,
		DialContext: (&net.Dialer{
			Timeout:   3 * time.Second,
			KeepAlive: 60 * time.Second,
		}).DialContext,
		MaxIdleConns:        200,
		MaxIdleConnsPerHost: 100,
		MaxConnsPerHost:     200,
		IdleConnTimeout:     90 * time.Second,
		DisableCompression:  true, // Navidrome в локальной сети, gzip только тратит процессор
	}

	reverseProxy := httputil.NewSingleHostReverseProxy(targetURL)
	reverseProxy.Transport = transport

	// Настройка Director с безопасной нормализацией заголовков X-Forwarded-* для работы за CDN/Reverse Proxy
	originalDirector := reverseProxy.Director
	director := SetupDirector(targetURL, originalDirector)
	reverseProxy.Director = func(req *http.Request) {
		director(req)
		// Ответ со списком расширений правим на лету, поэтому просим Navidrome не сжимать его (иначе poisq не добавится)
		if isExtensionsPath(req.URL.Path) {
			req.Header.Del("Accept-Encoding")
		}
	}

	// Гарантируем корректные CORS-заголовки для браузера при доступе через CDN/FRP с разными доменами
	reverseProxy.ModifyResponse = func(resp *http.Response) error {
		// Объявляем расширение poisq в getOpenSubsonicExtensions: так приложение узнаёт, что включать надстройку.
		if err := AdvertisePoisqExtension(resp); err != nil {
			return err
		}
		enrichArtistInfoResponse(resp, srv.biographies)
		if origin := resp.Request.Header.Get("Origin"); origin != "" {
			setCORS(resp.Header, origin)
		}
		return nil
	}

	memCache := cache.NewMemoryCache(2048, 45*time.Second)
	fanoutMgr := NewFanoutManager(targetURL, transport, memCache)
	interceptor := NewInterceptor(engine, fanoutMgr, reverseProxy)
	debugHandler := HandleDebugTest(engine, dict)
	aliasHandler := HandleAliasesAPI(aliasMgr)
	castAuth := newNavidromeAuth(targetURL, transport)
	mux := http.NewServeMux()

	// Все входящие запросы проходят централизованную маршрутизацию
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Безопасность: диагностические эндпоинты /_smart/* закрыты по умолчанию в production
		if strings.HasPrefix(r.URL.Path, "/_smart/") {
			if !enableDebugAPI {
				http.NotFound(w, r)
				return
			}

			if strings.HasPrefix(r.URL.Path, "/_smart/test") {
				debugHandler(w, r)
				return
			}
			if strings.HasPrefix(r.URL.Path, "/_smart/aliases") {
				aliasHandler(w, r)
				return
			}
			http.NotFound(w, r)
			return
		}

		// Собственные эндпоинты poisq: Navidrome их не знает, поэтому авторизацию проверяем сами
		if srv.cast != nil && cast.IsCastPath(r.URL.Path) {
			if !castAuth.check(r) {
				cast.WriteError(w, 40, "Wrong username or password")
				return
			}
			srv.cast.ServeHTTP(w, r)
			return
		}

		interceptor.ServeHTTP(w, r)
	})

	srv.handler = mux
	return srv
}

// setCORS разрешает браузерному клиенту с этого Origin работать с прокси (ответы и preflight).
func setCORS(h http.Header, origin string) {
	h.Set("Access-Control-Allow-Origin", origin)
	h.Set("Access-Control-Allow-Credentials", "true")
	h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS, HEAD")
	h.Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With, X-ND-Client-Unique-Id, X-ND-Authorization")
}

// CleanHost очищает строку хоста от порта и от запятых (в случае нескольких прокси в цепочке CDN/NPM/FRP).
func CleanHost(rawHost string) string {
	if rawHost == "" {
		return ""
	}
	// Если X-Forwarded-Host содержит список через запятую (например "muth.f5go.one, origin.f5go.one")
	if idx := strings.Index(rawHost, ","); idx != -1 {
		rawHost = rawHost[:idx]
	}
	return strings.TrimSpace(rawHost)
}

// ExtractClientHost определяет реальный домен клиента, приоритезируя Origin/Referer браузера и CDN-заголовки.
func ExtractClientHost(req *http.Request) string {
	if origin := req.Header.Get("Origin"); origin != "" {
		if u, err := url.Parse(origin); err == nil && u.Host != "" {
			return CleanHost(u.Host)
		}
	}
	if xfh := req.Header.Get("X-Forwarded-Host"); xfh != "" {
		if cleaned := CleanHost(xfh); cleaned != "" {
			return cleaned
		}
	}
	if referer := req.Header.Get("Referer"); referer != "" {
		if u, err := url.Parse(referer); err == nil && u.Host != "" {
			return CleanHost(u.Host)
		}
	}
	return CleanHost(req.Host)
}

// ExtractClientIP извлекает реальный IP клиента с учетом заголовков Cloudflare, Nginx и CDN.
func ExtractClientIP(req *http.Request) string {
	clientIP := req.Header.Get("CF-Connecting-IP")
	if clientIP == "" {
		clientIP = req.Header.Get("X-Real-IP")
	}
	if clientIP == "" {
		clientIP = req.RemoteAddr
		if host, _, err := net.SplitHostPort(req.RemoteAddr); err == nil {
			clientIP = host
		}
	}
	return clientIP
}

// SetupDirector настраивает функцию рерайтинга и нормализации заголовков для Reverse Proxy.
func SetupDirector(targetURL *url.URL, originalDirector func(*http.Request)) func(*http.Request) {
	return func(req *http.Request) {
		clientHost := ExtractClientHost(req)
		if clientHost == "" {
			clientHost = targetURL.Host
		}

		if originalDirector != nil {
			originalDirector(req)
		} else {
			req.URL.Scheme = targetURL.Scheme
			req.URL.Host = targetURL.Host
		}

		// Важно для CORS/CSRF и cookie в браузере при работе за CDN:
		// Host должен быть исходным хостом клиента (clientHost), а не внутренним именем контейнера
		req.Host = clientHost
		req.Header.Set("X-Forwarded-Host", clientHost)

		clientIP := ExtractClientIP(req)
		if prior := req.Header.Get("X-Forwarded-For"); prior != "" {
			req.Header.Set("X-Forwarded-For", prior+", "+clientIP)
		} else {
			req.Header.Set("X-Forwarded-For", clientIP)
		}

		// Корректная трансляция HTTPS протокола за Cloudflare и CDN
		isHTTPS := req.TLS != nil ||
			strings.EqualFold(req.Header.Get("X-Forwarded-Proto"), "https") ||
			strings.Contains(req.Header.Get("CF-Visitor"), "https")

		if isHTTPS {
			req.Header.Set("X-Forwarded-Proto", "https")
			req.Header.Set("X-Forwarded-Port", "443")
			req.Header.Set("X-Forwarded-Ssl", "on")
		} else if req.Header.Get("X-Forwarded-Proto") == "" {
			req.Header.Set("X-Forwarded-Proto", "http")
		}
	}
}

// ServeHTTP реализует интерфейс http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}
