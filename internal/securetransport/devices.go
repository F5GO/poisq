package securetransport

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// TokenReuseGrace — сколько после обновления токена ещё принимается предыдущий: ответ с новым токеном мог потеряться.
	TokenReuseGrace = 2 * time.Minute
	// MaxDevicesPerUser ограничивает число устройств одного аккаунта.
	MaxDevicesPerUser = 32

	maxDeviceName = 64
)

var (
	// ErrDeviceUnknown — токен не принадлежит ни одному устройству (отозвано или никогда не выдавалось).
	ErrDeviceUnknown = errors.New("secure transport: unknown device token")
	// ErrTokenReused — предъявлен уже заменённый токен вне окна TokenReuseGrace: устройство отозвано как скомпрометированное.
	ErrTokenReused = errors.New("secure transport: replaced device token reused")
	// ErrTooManyDevices — у аккаунта уже MaxDevicesPerUser устройств.
	ErrTooManyDevices = errors.New("secure transport: too many devices")
)

// Cipher шифрует пароль Navidrome в базе (security.KeyCipher).
type Cipher interface {
	Encrypt(plain string) (string, error)
	Decrypt(encoded string) (string, error)
}

// Device — устройство, вошедшее через Secure Transport.
type Device struct {
	ID         string    `json:"id"`
	Username   string    `json:"username"`
	Name       string    `json:"name"`
	CreatedAt  time.Time `json:"createdAt"`
	LastUsedAt time.Time `json:"lastUsedAt"`
}

// DeviceStore хранит устройства в базе poisq. Токен хранится только хэшем и меняется при каждом новом входе по нему.
type DeviceStore struct {
	db     *sql.DB
	cipher Cipher
	now    func() time.Time
}

// NewDeviceStore подключает хранилище устройств к базе poisq (таблица secure_devices).
func NewDeviceStore(db *sql.DB, cipher Cipher) *DeviceStore {
	return &DeviceStore{db: db, cipher: cipher, now: time.Now}
}

func tokenHash(token [32]byte) []byte { h := sha256.Sum256(token[:]); return h[:] }

func newToken() ([32]byte, error) {
	var t [32]byte
	_, err := rand.Read(t[:])
	return t, err
}

// CleanDeviceName оставляет печатные символы и ограничивает длину имени устройства.
func CleanDeviceName(name string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(name) {
		if r == utf8.RuneError || !unicode.IsPrint(r) {
			continue
		}
		if b.Len()+utf8.RuneLen(r) > maxDeviceName {
			break
		}
		b.WriteRune(r)
	}
	if b.Len() == 0 {
		return "Устройство"
	}
	return b.String()
}

// Create регистрирует устройство после успешной проверки пароля в Navidrome и возвращает его первый токен.
func (s *DeviceStore) Create(ctx context.Context, username, name, password string) (Device, [32]byte, error) {
	enc, err := s.cipher.Encrypt(password)
	if err != nil {
		return Device{}, [32]byte{}, err
	}
	token, err := newToken()
	if err != nil {
		return Device{}, [32]byte{}, err
	}
	var rawID [16]byte
	if _, err := rand.Read(rawID[:]); err != nil {
		return Device{}, [32]byte{}, err
	}
	now := s.now().UTC().Truncate(time.Second)
	d := Device{ID: hex.EncodeToString(rawID[:]), Username: username, Name: CleanDeviceName(name), CreatedAt: now, LastUsedAt: now}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Device{}, [32]byte{}, err
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM secure_devices WHERE username = ?", username).Scan(&count); err != nil {
		return Device{}, [32]byte{}, err
	}
	if count >= MaxDevicesPerUser {
		return Device{}, [32]byte{}, ErrTooManyDevices
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO secure_devices
		(id, username, name, token_hash, previous_token_hash, rotated_at, password_enc, created_at, last_used_at)
		VALUES (?, ?, ?, ?, NULL, ?, ?, ?, ?)`,
		d.ID, d.Username, d.Name, tokenHash(token), now.Unix(), enc, now.Unix(), now.Unix()); err != nil {
		return Device{}, [32]byte{}, err
	}
	return d, token, tx.Commit()
}

// Peek находит устройство по токену без изменений, по тем же правилам, что и Resume: так пароль проверяется
// в Navidrome до обновления токена, и недоступный Navidrome не оставляет клиента с невыданным новым токеном.
func (s *DeviceStore) Peek(ctx context.Context, token [32]byte) (Device, string, error) {
	h := tokenHash(token)
	var d Device
	var enc string
	err := s.db.QueryRowContext(ctx, "SELECT id, username, name, password_enc FROM secure_devices WHERE token_hash = ?", h).
		Scan(&d.ID, &d.Username, &d.Name, &enc)
	if errors.Is(err, sql.ErrNoRows) {
		var rotatedAt int64
		err = s.db.QueryRowContext(ctx, "SELECT id, username, name, password_enc, rotated_at FROM secure_devices WHERE previous_token_hash = ?", h).
			Scan(&d.ID, &d.Username, &d.Name, &enc, &rotatedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return Device{}, "", ErrDeviceUnknown
		}
		if err == nil && s.now().Sub(time.Unix(rotatedAt, 0)) > TokenReuseGrace {
			// Окончательное решение (удаление устройства) принимает Resume в транзакции.
			_, _, _, err = s.Resume(ctx, token)
			if err == nil {
				err = ErrTokenReused
			}
			return d, "", err
		}
	}
	if err != nil {
		return Device{}, "", err
	}
	password, err := s.cipher.Decrypt(enc)
	return d, password, err
}

// Resume проверяет токен устройства, выдаёт новый и возвращает пароль Navidrome для сессии.
// Заменённый токен принимается ещё TokenReuseGrace; позже он считается украденным, и устройство удаляется.
func (s *DeviceStore) Resume(ctx context.Context, token [32]byte) (Device, [32]byte, string, error) {
	h := tokenHash(token)
	now := s.now().UTC().Truncate(time.Second)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Device{}, [32]byte{}, "", err
	}
	defer tx.Rollback()

	var d Device
	var enc string
	var rotatedAt, created, lastUsed int64
	var previous []byte
	err = tx.QueryRowContext(ctx, `SELECT id, username, name, password_enc, rotated_at, created_at, last_used_at, previous_token_hash
		FROM secure_devices WHERE token_hash = ?`, h).Scan(&d.ID, &d.Username, &d.Name, &enc, &rotatedAt, &created, &lastUsed, &previous)
	keepPrevious := h
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRowContext(ctx, `SELECT id, username, name, password_enc, rotated_at, created_at, last_used_at
			FROM secure_devices WHERE previous_token_hash = ?`, h).Scan(&d.ID, &d.Username, &d.Name, &enc, &rotatedAt, &created, &lastUsed)
		if errors.Is(err, sql.ErrNoRows) {
			return Device{}, [32]byte{}, "", ErrDeviceUnknown
		}
		if err != nil {
			return Device{}, [32]byte{}, "", err
		}
		if now.Sub(time.Unix(rotatedAt, 0)) > TokenReuseGrace {
			if _, err := tx.ExecContext(ctx, "DELETE FROM secure_devices WHERE id = ?", d.ID); err != nil {
				return Device{}, [32]byte{}, "", err
			}
			if err := tx.Commit(); err != nil {
				return Device{}, [32]byte{}, "", err
			}
			return d, [32]byte{}, "", ErrTokenReused
		}
		// Окно повтора: прежний токен остаётся предыдущим, а выданный после него (не дошедший до клиента) пропадает.
		// Время обновления не сдвигается, иначе старый токен можно было бы продлевать бесконечно.
	} else if err != nil {
		return Device{}, [32]byte{}, "", err
	} else {
		rotatedAt = now.Unix()
	}
	password, err := s.cipher.Decrypt(enc)
	if err != nil {
		return Device{}, [32]byte{}, "", err
	}
	next, err := newToken()
	if err != nil {
		return Device{}, [32]byte{}, "", err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE secure_devices SET token_hash = ?, previous_token_hash = ?, rotated_at = ?, last_used_at = ?
		WHERE id = ?`, tokenHash(next), keepPrevious, rotatedAt, now.Unix(), d.ID); err != nil {
		return Device{}, [32]byte{}, "", err
	}
	if err := tx.Commit(); err != nil {
		return Device{}, [32]byte{}, "", err
	}
	d.CreatedAt, d.LastUsedAt = time.Unix(created, 0).UTC(), now
	return d, next, password, nil
}

// Credentials возвращает пользователя и пароль Navidrome действующего устройства (для офлайн-загрузок).
func (s *DeviceStore) Credentials(ctx context.Context, id string) (string, string, error) {
	var user, enc string
	err := s.db.QueryRowContext(ctx, "SELECT username, password_enc FROM secure_devices WHERE id = ?", id).Scan(&user, &enc)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrDeviceUnknown
	}
	if err != nil {
		return "", "", err
	}
	password, err := s.cipher.Decrypt(enc)
	return user, password, err
}

// List возвращает устройства, новые входы первыми.
func (s *DeviceStore) List(ctx context.Context) ([]Device, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, username, name, created_at, last_used_at FROM secure_devices
		ORDER BY last_used_at DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Device
	for rows.Next() {
		var d Device
		var created, lastUsed int64
		if err := rows.Scan(&d.ID, &d.Username, &d.Name, &created, &lastUsed); err != nil {
			return nil, err
		}
		d.CreatedAt, d.LastUsedAt = time.Unix(created, 0).UTC(), time.Unix(lastUsed, 0).UTC()
		out = append(out, d)
	}
	return out, rows.Err()
}

// Revoke удаляет устройство. Возвращает false, если такого нет.
func (s *DeviceStore) Revoke(ctx context.Context, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx, "DELETE FROM secure_devices WHERE id = ?", id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// RevokeUser удаляет все устройства аккаунта (смена пароля или удаление пользователя в Navidrome) и возвращает их ID.
func (s *DeviceStore) RevokeUser(ctx context.Context, username string) ([]string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT id FROM secure_devices WHERE username = ?", username)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if _, err := tx.ExecContext(ctx, "DELETE FROM secure_devices WHERE username = ?", username); err != nil {
		return nil, err
	}
	return ids, tx.Commit()
}
