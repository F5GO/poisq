// Package security отвечает за криптографическую защиту API-ключей, проверку путей
// файловой системы медиатеки (/music) и защиту административного контура (:4535).
//
// Данный файл security_test.go содержит модульные тесты шифрования AES-256-GCM,
// маскирования ключей и защиты от Path Traversal / Symlink Escape.
package security

import (
	"os"
	"path/filepath"
	"testing"
)

func TestKeyCipherEncryptDecrypt(t *testing.T) {
	cipher, err := NewKeyCipher("test-master-secret-key-2026", t.TempDir())
	if err != nil {
		t.Fatalf("NewKeyCipher error: %v", err)
	}

	rawKey := "AIzaSyD1234567890abcdefXYZ_secret"
	encrypted, err := cipher.Encrypt(rawKey)
	if err != nil {
		t.Fatalf("Encrypt error: %v", err)
	}
	if encrypted == rawKey {
		t.Fatalf("encrypted key must not equal plaintext")
	}

	decrypted, err := cipher.Decrypt(encrypted)
	if err != nil {
		t.Fatalf("Decrypt error: %v", err)
	}
	if decrypted != rawKey {
		t.Fatalf("got %q, want %q", decrypted, rawKey)
	}

	masked := MaskKey(rawKey)
	if masked != "AIza••••••cret" {
		t.Fatalf("unexpected mask: %q", masked)
	}
}

func TestPathGuardPreventsTraversalAndForbiddenExts(t *testing.T) {
	musicRoot := t.TempDir()
	guard, err := NewPathGuard(musicRoot)
	if err != nil {
		t.Fatalf("NewPathGuard error: %v", err)
	}

	// Валидный путь к .lrc внутри /music
	validLrc := filepath.Join(musicRoot, "Kino", "Gruppa_Krovi.lrc")
	if _, err := guard.ValidateTarget(validLrc, true); err != nil {
		t.Fatalf("expected valid path, got error: %v", err)
	}

	// Попытка выхода наружу через ../
	traversalPath := filepath.Join(musicRoot, "..", "etc", "passwd")
	if _, err := guard.ValidateTarget(traversalPath, false); err == nil {
		t.Fatalf("expected traversal error for %s", traversalPath)
	}

	// Попытка записи исполняемого скрипта .sh внутри /music
	badExtPath := filepath.Join(musicRoot, "Artist", "exploit.sh")
	if _, err := guard.ValidateTarget(badExtPath, true); err == nil {
		t.Fatalf("expected forbidden extension error for .sh")
	}

	// Попытка обхода через симлинк наружу
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "secret.flac")
	_ = os.WriteFile(outsideFile, []byte("test"), 0644)

	symlinkPath := filepath.Join(musicRoot, "evil_link.flac")
	if err := os.Symlink(outsideFile, symlinkPath); err == nil {
		if _, err := guard.ValidateTarget(symlinkPath, true); err == nil {
			t.Fatalf("expected symlink escape to be blocked")
		}
	}
}

func TestIsAdminHost(t *testing.T) {
	cases := map[string]bool{
		"localhost:4535": true, "127.0.0.1:4535": true, "[::1]:4535": true, "192.168.1.10:4535": true,
		"nas:4535": true, "nas.local:4535": true, "studio.example.com": true,
		"evil.com:4535": false, "localhost.evil.com:4535": false, "": false,
	}
	for host, want := range cases {
		if got := isAdminHost(host, []string{" Studio.Example.com "}); got != want {
			t.Errorf("isAdminHost(%q) = %v, want %v", host, got, want)
		}
	}
}
