package securetransport

import (
	"errors"
	"sync"
	"time"
)

// Installation — включённость Secure Transport, внешний адрес и ключи установки, общие для прокси и панели Studio.
// Ключи создаются только при первом включении и сохраняются при выключении, чтобы QR не менялся.
type Installation struct {
	dir string

	mu      sync.RWMutex
	enabled bool
	origin  string
	keys    *KeyStore
}

// NewInstallation готовит состояние; dir — папка ключей (создаётся при первом включении).
func NewInstallation(dir string) *Installation { return &Installation{dir: dir} }

// Configure проверяет и применяет настройки. Включение требует корректного внешнего HTTPS-адреса.
// При ошибке прежнее состояние не меняется.
func (i *Installation) Configure(enabled bool, publicOrigin string, now time.Time) error {
	origin := ""
	if publicOrigin != "" {
		canonical, err := CanonicalOrigin(publicOrigin)
		if err != nil {
			return err
		}
		origin = canonical
	}
	if enabled && origin == "" {
		return errors.New("secure transport requires a public HTTPS origin")
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if enabled && i.keys == nil {
		keys, err := OpenKeyStore(i.dir, now)
		if err != nil {
			return err
		}
		i.keys = keys
	}
	i.enabled, i.origin = enabled, origin
	return nil
}

// State возвращает настройки и ключи; keys — nil, пока Secure Transport ни разу не включали.
func (i *Installation) State() (enabled bool, origin string, keys *KeyStore) {
	if i == nil {
		return false, "", nil
	}
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.enabled, i.origin, i.keys
}

// Rotate обновляет рабочие ключи, если они уже созданы.
func (i *Installation) Rotate(now time.Time) error {
	if _, _, keys := i.State(); keys != nil {
		_, err := keys.Rotate(now)
		return err
	}
	return nil
}
