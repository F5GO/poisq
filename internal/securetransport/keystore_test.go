package securetransport

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

var storeStart = time.Unix(1767225600, 0).UTC()

func TestKeyStoreCreatesAndReloadsIdentity(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "secure-transport")
	s, err := OpenKeyStore(dir, storeStart)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{keysFile, nextRootFile} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s: mode %v, %v", name, info.Mode().Perm(), err)
		}
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("key directory mode %v, %v", info.Mode().Perm(), err)
	}
	raw := s.CurrentCertificate()
	cert, err := ParseWorkingCertificate(raw, s.RootPublicKey(), s.InstallationID())
	if err != nil || cert.Epoch != 1 || !cert.ValidAt(storeStart) {
		t.Fatalf("initial certificate %+v, %v", cert, err)
	}
	private, ok := s.WorkingKey(1, storeStart)
	if !ok || [32]byte(private.PublicKey().Bytes()) != cert.WorkingKey {
		t.Fatal("working key does not match its certificate")
	}
	code, err := s.PairingCode("https://music.example.com")
	if err != nil || code == "" {
		t.Fatalf("pairing code: %q, %v", code, err)
	}

	reopened, err := OpenKeyStore(dir, storeStart.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if reopened.InstallationID() != s.InstallationID() || !reopened.RootPublicKey().Equal(s.RootPublicKey()) ||
		string(reopened.CurrentCertificate()) != string(raw) {
		t.Fatal("restart changed the installation identity or working key")
	}
}

func TestKeyStoreRotatesWithOverlap(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenKeyStore(dir, storeStart)
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := s.Rotate(storeStart.Add(3 * 24 * time.Hour)); err != nil || changed {
		t.Fatalf("rotated too early: %v, %v", changed, err)
	}
	renew := storeStart.Add(WorkingKeyLifetime - WorkingKeyRenewBefore + time.Second)
	if changed, err := s.Rotate(renew); err != nil || !changed {
		t.Fatalf("did not rotate: %v, %v", changed, err)
	}
	cert, err := ParseWorkingCertificate(s.CurrentCertificate(), s.RootPublicKey(), s.InstallationID())
	if err != nil || cert.Epoch != 2 {
		t.Fatalf("current epoch %d, %v", cert.Epoch, err)
	}
	if _, ok := s.WorkingKey(1, renew); !ok {
		t.Fatal("previous epoch must still be accepted during the overlap")
	}
	if _, ok := s.WorkingKey(2, renew); !ok {
		t.Fatal("new epoch is not accepted")
	}
	expired := storeStart.Add(WorkingKeyLifetime)
	if _, err := s.Rotate(expired); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.WorkingKey(1, expired); ok {
		t.Fatal("expired epoch is still accepted")
	}
	reopened, err := OpenKeyStore(dir, expired)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reopened.WorkingKey(1, expired); ok {
		t.Fatal("expired epoch survived a restart")
	}
	if _, ok := reopened.WorkingKey(2, expired); !ok {
		t.Fatal("current epoch lost after restart")
	}
}

func TestKeyStoreIssuesFreshKeyAfterLongDowntime(t *testing.T) {
	dir := t.TempDir()
	if _, err := OpenKeyStore(dir, storeStart); err != nil {
		t.Fatal(err)
	}
	later := storeStart.Add(90 * 24 * time.Hour)
	s, err := OpenKeyStore(dir, later)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := ParseWorkingCertificate(s.CurrentCertificate(), s.RootPublicKey(), s.InstallationID())
	if err != nil || cert.Epoch != 2 || !cert.ValidAt(later) {
		t.Fatalf("certificate after downtime %+v, %v", cert, err)
	}
}

func TestKeyStoreRejectsCorruptFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, keysFile), []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenKeyStore(dir, storeStart); err == nil {
		t.Fatal("opened a corrupt key store")
	}
	if _, err := OpenKeyStore("", storeStart); err == nil {
		t.Fatal("opened a key store without a directory")
	}
}

func TestKeyStoreTightensPermissions(t *testing.T) {
	dir := t.TempDir()
	if _, err := OpenKeyStore(dir, storeStart); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, keysFile), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenKeyStore(dir, storeStart); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(filepath.Join(dir, keysFile)); info.Mode().Perm() != 0o600 {
		t.Fatalf("keys file mode %v", info.Mode().Perm())
	}
}
