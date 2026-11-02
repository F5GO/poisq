// Package security отвечает за криптографическую защиту API-ключей, проверку путей
// файловой системы медиатеки (/music) и защиту административного контура (:4535).
//
// Данный файл crypto.go реализует шифрование и расшифровку API-ключей алгоритмом
// AES-256-GCM с автоматической инициализацией мастер-ключа и безопасным маскированием
// токенов для отображения в веб-интерфейсе.
package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// KeyCipher управляет шифрованием и расшифровкой секретных API-ключей в БД.
type KeyCipher struct {
	gcm cipher.AEAD
}

// NewKeyCipher инициализирует AES-256-GCM шифратор из переданного секрета
// или загружает/генерирует постоянный ключ рядом с базой данных poisq.db.
func NewKeyCipher(secret string, dbPath string) (*KeyCipher, error) {
	rawSecret := strings.TrimSpace(secret)
	if rawSecret == "" {
		loaded, err := loadOrCreateSecretFile(dbPath)
		if err != nil {
			return nil, fmt.Errorf("не удалось инициализировать мастер-ключ шифрования: %w", err)
		}
		rawSecret = loaded
	}

	hash := sha256.Sum256([]byte(rawSecret))
	block, err := aes.NewCipher(hash[:])
	if err != nil {
		return nil, fmt.Errorf("ошибка инициализации AES-256: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("ошибка инициализации GCM: %w", err)
	}

	return &KeyCipher{gcm: gcm}, nil
}

// Encrypt шифрует открытый API-ключ и возвращает строку в формате Base64.
func (c *KeyCipher) Encrypt(plainKey string) (string, error) {
	if plainKey == "" {
		return "", errors.New("пустой API-ключ не может быть зашифрован")
	}

	nonce := make([]byte, c.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("ошибка генерации nonce: %w", err)
	}

	ciphertext := c.gcm.Seal(nonce, nonce, []byte(plainKey), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// Decrypt расшифровывает строку Base64 обратно в исходный API-ключ.
func (c *KeyCipher) Decrypt(encoded string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("некорректный формат зашифрованного ключа: %w", err)
	}

	nonceSize := c.gcm.NonceSize()
	if len(data) < nonceSize {
		return "", errors.New("зашифрованные данные повреждены (слишком короткий буфер)")
	}

	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plain, err := c.gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("ошибка расшифровки API-ключа (неверный мастер-ключ): %w", err)
	}

	return string(plain), nil
}

// MaskKey формирует безопасную маску ключа для отображения в UI (например, "AIza••••••7x9Q").
func MaskKey(plainKey string) string {
	trimmed := strings.TrimSpace(plainKey)
	runes := []rune(trimmed)
	n := len(runes)
	if n == 0 {
		return ""
	}
	if n <= 8 {
		return string(runes[:1]) + "••••••"
	}
	return string(runes[:4]) + "••••••" + string(runes[n-4:])
}

func loadOrCreateSecretFile(dbPath string) (string, error) {
	dir := filepath.Dir(dbPath)
	if dir == "" || dir == "." {
		dir = os.TempDir()
	}
	_ = os.MkdirAll(dir, 0755)

	keyFile := filepath.Join(dir, ".poisq_master_key")
	if data, err := os.ReadFile(keyFile); err == nil && len(strings.TrimSpace(string(data))) >= 32 {
		return strings.TrimSpace(string(data)), nil
	}

	buf := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(buf)
	if err := os.WriteFile(keyFile, []byte(encoded), 0600); err != nil {
		return "", err
	}
	return encoded, nil
}
