// Package keypool управляет пулом API-ключей (Gemini, Groq, AcoustID, Yandex, Genius)
// с персистентной памятью в SQLite, равномерным распределением по алгоритму LRU
// и автоматической обработкой лимитов (65 сек cooldown и сброс дневных квот в полночь по PST).
//
// Данный файл manager.go реализует менеджер ротации ключей, шифрование при добавлении,
// расшифровку на лету при выдаче воркерам и классификацию ошибок внешних API.
package keypool

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/F5GO/poisq/internal/db"
	"github.com/F5GO/poisq/internal/security"
)

const (
	// CooldownRateLimitDuration — длительность минутного кулдауна при HTTP 429 (65 секунд по ТЗ).
	CooldownRateLimitDuration = 65 * time.Second
)

// LeasedKey содержит выданный воркеру расшифрованный ключ и его ID в базе данных.
type LeasedKey struct {
	ID       int64
	Provider string
	Label    string
	PlainKey string
	KeyMask  string
}

// Manager координирует безопасный доступ к пулу API-ключей.
type Manager struct {
	mu     sync.Mutex
	repo   *db.KeyRepository
	cipher *security.KeyCipher
}

// NewManager создает новый менеджер пула API-ключей.
func NewManager(repo *db.KeyRepository, cipher *security.KeyCipher) *Manager {
	return &Manager{
		repo:   repo,
		cipher: cipher,
	}
}

// AddKey шифрует новый API-ключ и сохраняет его в таблицу api_keys.
func (m *Manager) AddKey(provider, label, plainKey string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	cleanProvider := strings.ToLower(strings.TrimSpace(provider))
	cleanKey := strings.TrimSpace(plainKey)
	if cleanProvider == "" || cleanKey == "" {
		return 0, errors.New("провайдер и API-ключ не могут быть пустыми")
	}

	encrypted, err := m.cipher.Encrypt(cleanKey)
	if err != nil {
		return 0, err
	}
	mask := security.MaskKey(cleanKey)
	return m.repo.AddKey(cleanProvider, strings.TrimSpace(label), encrypted, mask)
}

// DeleteKey удаляет ключ из пула.
func (m *Manager) DeleteKey(id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.repo.DeleteKey(id)
}

// ListKeys возвращает все ключи (в маскированном виде) для панели управления.
func (m *Manager) ListKeys() ([]db.APIKeyRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.repo.ListAll()
}

// Acquire выбирает следующий активный ключ провайдера по алгоритму LRU и расшифровывает его в памяти.
func (m *Manager) Acquire(provider string) (*LeasedKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	cleanProvider := strings.ToLower(strings.TrimSpace(provider))
	rec, err := m.repo.AcquireLRUKey(cleanProvider, time.Now().Unix())
	if err != nil {
		return nil, err
	}

	plain, err := m.cipher.Decrypt(rec.KeyEncrypted)
	if err != nil {
		_ = m.repo.MarkStatus(rec.ID, "invalid", 0, "ошибка расшифровки ключа: "+err.Error())
		return nil, fmt.Errorf("ключ #%d поврежден: %w", rec.ID, err)
	}

	return &LeasedKey{
		ID:       rec.ID,
		Provider: rec.Provider,
		Label:    rec.Label,
		PlainKey: plain,
		KeyMask:  rec.KeyMask,
	}, nil
}

// ReportSuccess фиксирует успешное выполнение запроса с данным ключом.
func (m *Manager) ReportSuccess(keyID int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_ = m.repo.MarkSuccess(keyID)
}

// ReportRateLimit429 переводит ключ в минутный cooldown на 65 секунд.
func (m *Manager) ReportRateLimit429(keyID int64, details string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	until := time.Now().Add(CooldownRateLimitDuration).Unix()
	msg := "429 Too Many Requests (cooldown 65s)"
	if details != "" {
		msg += ": " + details
	}
	_ = m.repo.MarkStatus(keyID, "cooldown", until, msg)
}

// ReportUnavailable503 переводит ключ в короткий 20-секундный cooldown при перегрузке или временной недоступности (HTTP 503 / 502 / 504), фиксируя ошибку в статистике пула.
func (m *Manager) ReportUnavailable503(keyID int64, details string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	until := time.Now().Add(20 * time.Second).Unix()
	msg := "503 Unavailable (высокая нагрузка / high demand)"
	if details != "" {
		msg += ": " + details
	}
	_ = m.repo.MarkStatus(keyID, "cooldown", until, msg)
}

// ReportDailyQuotaExhausted переводит ключ в статус exhausted_daily до сброса квоты в полночь по PST (~11:00 МСК).
func (m *Manager) ReportDailyQuotaExhausted(keyID int64, details string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	until := NextPSTMidnight(time.Now()).Unix()
	msg := "RESOURCE_EXHAUSTED (дневная квота исчерпана до полуночи PST)"
	if details != "" {
		msg += ": " + details
	}
	_ = m.repo.MarkStatus(keyID, "exhausted_daily", until, msg)
}

// ReportInvalid переводит ключ в фатальный статус invalid (при ошибках 400 API_KEY_INVALID / 401 / 403).
func (m *Manager) ReportInvalid(keyID int64, httpCode int, details string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	msg := fmt.Sprintf("Фатальная ошибка авторизации HTTP %d", httpCode)
	if details != "" {
		msg += ": " + details
	}
	_ = m.repo.MarkStatus(keyID, "invalid", 0, msg)
}

// HasKeys возвращает true, если для данного провайдера в пуле есть хотя бы один зарегистрированный ключ.
func (m *Manager) HasKeys(provider string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	keys, err := m.repo.ListAll()
	if err != nil {
		return false
	}
	clean := strings.ToLower(strings.TrimSpace(provider))
	for _, k := range keys {
		if strings.ToLower(k.Provider) == clean {
			return true
		}
	}
	return false
}

// NextRetryTimestamp возвращает время освобождения ближайшего ключа для провайдера.
func (m *Manager) NextRetryTimestamp(provider string) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()

	ts := m.repo.NextAvailableTime(strings.ToLower(strings.TrimSpace(provider)))
	if ts <= 0 {
		return time.Now().Add(65 * time.Second).Unix()
	}
	return ts
}
