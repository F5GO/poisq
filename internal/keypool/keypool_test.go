// Package keypool управляет пулом API-ключей (Gemini, Groq, Yandex, Genius)
// с персистентной памятью в SQLite, равномерным распределением по алгоритму LRU
// и автоматической обработкой лимитов (65 сек cooldown и сброс дневных квот в полночь по PST).
//
// Данный файл keypool_test.go проверяет работу LRU-ротации, переключение ключей при 429 cooldown,
// обработку исчерпания дневной квоты (RESOURCE_EXHAUSTED) и блокировку невалидных ключей.
package keypool

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/F5GO/poisq/internal/db"
	"github.com/F5GO/poisq/internal/security"
)

func TestKeyPoolLRURotationAndCooldowns(t *testing.T) {
	tmpDir := t.TempDir()
	sqliteDB, err := db.OpenPoisqDatabase(filepath.Join(tmpDir, "poisq.db"))
	if err != nil {
		t.Fatalf("OpenPoisqDatabase error: %v", err)
	}
	defer sqliteDB.Close()

	cipher, err := security.NewKeyCipher("unit-test-secret", tmpDir)
	if err != nil {
		t.Fatalf("NewKeyCipher error: %v", err)
	}

	repo := db.NewKeyRepository(sqliteDB)
	mgr := NewManager(repo, cipher)

	id1, err := mgr.AddKey("gemini", "Key 1", "AIzaSyKeyOne111111")
	if err != nil {
		t.Fatalf("AddKey 1: %v", err)
	}
	id2, err := mgr.AddKey("gemini", "Key 2", "AIzaSyKeyTwo222222")
	if err != nil {
		t.Fatalf("AddKey 2: %v", err)
	}

	// Первая выборка должна вернуть Key 1
	k1, err := mgr.Acquire("gemini")
	if err != nil || k1.ID != id1 || k1.PlainKey != "AIzaSyKeyOne111111" {
		t.Fatalf("expected Key 1, got %+v (err=%v)", k1, err)
	}

	// Переводим Key 1 в 429 Cooldown на 65 секунд -> следующий вызов мгновенно отдает Key 2
	mgr.ReportRateLimit429(k1.ID, "RPM limit reached")

	k2, err := mgr.Acquire("gemini")
	if err != nil || k2.ID != id2 {
		t.Fatalf("expected instant switch to Key 2, got %+v (err=%v)", k2, err)
	}

	// Переводим Key 2 в exhausted_daily -> больше активных ключей нет, очередь должна встать на паузу
	mgr.ReportDailyQuotaExhausted(k2.ID, "Daily RPD quota exceeded")

	if _, err := mgr.Acquire("gemini"); err == nil {
		t.Fatalf("expected ErrNoAvailableKeys when all keys are in cooldown/exhausted")
	}

	nextTs := mgr.NextRetryTimestamp("gemini")
	if nextTs <= time.Now().Unix() {
		t.Fatalf("expected future retry timestamp, got %d", nextTs)
	}
}

// Ключ, не расшифрованный чужим мастер-ключом, возвращается в строй после перезапуска с верным.
func TestDecryptErrorRecoversAfterRestartWithRightSecret(t *testing.T) {
	tmpDir := t.TempDir()
	sqliteDB, err := db.OpenPoisqDatabase(filepath.Join(tmpDir, "poisq.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sqliteDB.Close()
	repo := db.NewKeyRepository(sqliteDB)
	right, _ := security.NewKeyCipher("right-secret", tmpDir)
	wrong, _ := security.NewKeyCipher("wrong-secret", tmpDir)

	if _, err := NewManager(repo, right).AddKey("groq", "k", "gsk_value"); err != nil {
		t.Fatal(err)
	}
	// старый формат: раньше такой ключ навсегда становился invalid
	if _, err := sqliteDB.Exec(`INSERT INTO api_keys (provider, label, key_encrypted, key_mask, status, last_error) VALUES ('groq', 'old', ?, 'x', 'invalid', 'ошибка расшифровки ключа: test')`, mustEncrypt(t, right, "gsk_old")); err != nil {
		t.Fatal(err)
	}

	bad := NewManager(repo, wrong)
	if _, err := bad.Acquire("groq"); err == nil {
		t.Fatal("чужой мастер-ключ не должен расшифровать ключ")
	}
	if bad.HasKeys("groq") {
		t.Fatal("нерасшифрованные ключи не должны считаться рабочими")
	}

	good := NewManager(repo, right)
	seen := map[string]bool{}
	var lastID int64
	for i := 0; i < 2; i++ {
		k, err := good.AcquireExcluding("groq", lastID)
		if err != nil {
			t.Fatalf("после перезапуска с верным ключом: %v", err)
		}
		seen[k.PlainKey] = true
		lastID = k.ID
	}
	if !seen["gsk_value"] || !seen["gsk_old"] {
		t.Fatalf("в строй вернулись не все ключи: %v", seen)
	}
}

func mustEncrypt(t *testing.T, c *security.KeyCipher, v string) string {
	t.Helper()
	s, err := c.Encrypt(v)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
