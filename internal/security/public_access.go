package security

import (
	"path"
	"strings"
	"sync/atomic"
)

// PublicAccess — политика публичного Subsonic-входа (:4534), общая для прокси и панели Studio.
// Нулевое значение и nil закрывают веб-интерфейс Navidrome: при любой ошибке настройки вход остаётся закрытым.
type PublicAccess struct {
	navidromeWebOpen atomic.Bool
}

// NavidromeWebOpen сообщает, пропускает ли :4534 запросы к веб-интерфейсу и собственному API Navidrome.
func (a *PublicAccess) NavidromeWebOpen() bool { return a != nil && a.navidromeWebOpen.Load() }

// SetNavidromeWebOpen меняет политику без перезапуска прокси.
func (a *PublicAccess) SetNavidromeWebOpen(open bool) { a.navidromeWebOpen.Store(open) }

// IsSubsonicPath разрешает только канонический путь Subsonic API: /rest/<метод>.
// Неоднозначные пути (.., ., //, закодированные символы, \, другой регистр префикса) отвергаются,
// а не исправляются: так проверенный путь совпадает с тем, что уходит в Navidrome.
// rawPath — URL.RawPath запроса; непустое значение означает, что путь пришёл в закодированном виде.
func IsSubsonicPath(p, rawPath string) bool {
	if rawPath != "" || len(p) > 256 || !strings.HasPrefix(p, "/rest/") || path.Clean(p) != p {
		return false
	}
	for i := len("/rest/"); i < len(p); i++ {
		c := p[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
