// Package db отвечает за безопасное взаимодействие с базами данных SQLite (navidrome.db и poisq.db).
//
// Данный файл repo_keys.go реализует персистентный репозиторий таблицы api_keys
// для хранения, LRU-выборки и обновления статусов ротации ключей (Gemini, Groq, Yandex).
package db

import (
	"database/sql"
	"errors"
	"time"
)

// ErrNoAvailableKeys сигнализирует об отсутствии активных ключей для указанного провайдера.
var ErrNoAvailableKeys = errors.New("нет доступных активных ключей для провайдера")

// APIKeyRecord представляет запись ключа в таблице api_keys.
type APIKeyRecord struct {
	ID            int64  `json:"id"`
	Provider      string `json:"provider"`
	Label         string `json:"label"`
	KeyEncrypted  string `json:"-"`
	KeyMask       string `json:"keyMask"`
	Status        string `json:"status"`
	CooldownUntil int64  `json:"cooldownUntil"`
	LastUsedAt    int64  `json:"lastUsedAt"`
	SuccessCount  int64  `json:"successCount"`
	ErrorCount    int64  `json:"errorCount"`
	LastError     string `json:"lastError"`
	CreatedAt     int64  `json:"createdAt"`
}

// KeyRepository инкапсулирует SQL-операции над таблицей api_keys.
type KeyRepository struct {
	db *sql.DB
}

// NewKeyRepository создает новый репозиторий ключей.
func NewKeyRepository(db *sql.DB) *KeyRepository {
	return &KeyRepository{db: db}
}

// AddKey добавляет новый зашифрованный ключ в пул.
func (r *KeyRepository) AddKey(provider, label, encryptedKey, maskedKey string) (int64, error) {
	now := time.Now().Unix()
	res, err := r.db.Exec(`
		INSERT INTO api_keys (provider, label, key_encrypted, key_mask, status, created_at)
		VALUES (?, ?, ?, ?, 'active', ?)
	`, provider, label, encryptedKey, maskedKey, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// DeleteKey удаляет ключ по его ID.
func (r *KeyRepository) DeleteKey(id int64) error {
	_, err := r.db.Exec(`DELETE FROM api_keys WHERE id = ?`, id)
	return err
}

// ListAll возвращает все ключи для отображения в веб-интерфейсе, предварительно снимая истекшие кулдауны.
func (r *KeyRepository) ListAll() ([]APIKeyRecord, error) {
	now := time.Now().Unix()
	_, _ = r.db.Exec(`
		UPDATE api_keys
		SET status = 'active', cooldown_until = 0
		WHERE status IN ('cooldown', 'exhausted_daily') AND cooldown_until > 0 AND cooldown_until <= ?
	`, now)

	rows, err := r.db.Query(`
		SELECT id, provider, label, key_encrypted, key_mask, status, cooldown_until,
		       last_used_at, success_count, error_count, last_error, created_at
		FROM api_keys
		ORDER BY provider ASC, id ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []APIKeyRecord
	for rows.Next() {
		var k APIKeyRecord
		if err := rows.Scan(
			&k.ID, &k.Provider, &k.Label, &k.KeyEncrypted, &k.KeyMask, &k.Status, &k.CooldownUntil,
			&k.LastUsedAt, &k.SuccessCount, &k.ErrorCount, &k.LastError, &k.CreatedAt,
		); err != nil {
			return nil, err
		}
		list = append(list, k)
	}
	return list, rows.Err()
}

// AcquireLRUKey атомарно размораживает ключи с истекшим кулдауном и выбирает наиболее давно
// использовавшийся активный ключ (алгоритм LRU), обновляя его last_used_at.
func (r *KeyRepository) AcquireLRUKey(provider string, nowUnix int64) (*APIKeyRecord, error) {
	return r.AcquireLRUKeyExcluding(provider, nowUnix, 0)
}

// AcquireLRUKeyExcluding выбирает активный ключ по LRU, отдавая предпочтение ключам,
// отличным от excludeID (если в пуле есть другие активные ключи).
func (r *KeyRepository) AcquireLRUKeyExcluding(provider string, nowUnix int64, excludeID int64) (*APIKeyRecord, error) {
	// 1. Возвращаем в строй ключи, у которых истек минутный или дневной кулдаун
	_, _ = r.db.Exec(`
		UPDATE api_keys
		SET status = 'active', cooldown_until = 0
		WHERE provider = ? AND status IN ('cooldown', 'exhausted_daily')
		  AND cooldown_until > 0 AND cooldown_until <= ?
	`, provider, nowUnix)

	// 2. Выбираем активный ключ с минимальным last_used_at (Least Recently Used), избегая excludeID при наличии альтернатив
	var k APIKeyRecord
	err := r.db.QueryRow(`
		SELECT id, provider, label, key_encrypted, key_mask, status, cooldown_until,
		       last_used_at, success_count, error_count, last_error, created_at
		FROM api_keys
		WHERE provider = ? AND status = 'active'
		ORDER BY CASE WHEN id = ? THEN 1 ELSE 0 END ASC, last_used_at ASC, id ASC
		LIMIT 1
	`, provider, excludeID).Scan(
		&k.ID, &k.Provider, &k.Label, &k.KeyEncrypted, &k.KeyMask, &k.Status, &k.CooldownUntil,
		&k.LastUsedAt, &k.SuccessCount, &k.ErrorCount, &k.LastError, &k.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNoAvailableKeys
		}
		return nil, err
	}

	_, _ = r.db.Exec(`UPDATE api_keys SET last_used_at = ? WHERE id = ?`, nowUnix, k.ID)
	k.LastUsedAt = nowUnix
	return &k, nil
}

// MarkSuccess увеличивает счетчик успешных вызовов ключа.
func (r *KeyRepository) MarkSuccess(id int64) error {
	_, err := r.db.Exec(`
		UPDATE api_keys
		SET success_count = success_count + 1, status = 'active', cooldown_until = 0, last_error = ''
		WHERE id = ?
	`, id)
	return err
}

// MarkStatus переводит ключ в указанный статус (cooldown, exhausted_daily, invalid) с логом ошибки.
func (r *KeyRepository) MarkStatus(id int64, status string, cooldownUntil int64, errMsg string) error {
	_, err := r.db.Exec(`
		UPDATE api_keys
		SET status = ?, cooldown_until = ?, error_count = error_count + 1, last_error = ?
		WHERE id = ?
	`, status, cooldownUntil, errMsg, id)
	return err
}

// NextAvailableTime возвращает минимальный timestamp окончания кулдауна для провайдера (если все ключи в кулдауне).
func (r *KeyRepository) NextAvailableTime(provider string) int64 {
	var minCooldown sql.NullInt64
	_ = r.db.QueryRow(`
		SELECT MIN(cooldown_until)
		FROM api_keys
		WHERE provider = ? AND status IN ('cooldown', 'exhausted_daily') AND cooldown_until > 0
	`, provider).Scan(&minCooldown)
	if minCooldown.Valid {
		return minCooldown.Int64
	}
	return 0
}
