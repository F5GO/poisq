package securetransport

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	// WorkingKeyLifetime — срок сертификата рабочего ключа.
	WorkingKeyLifetime = 7 * 24 * time.Hour
	// WorkingKeyRenewBefore — за сколько до истечения выпускается следующая эпоха; обе принимаются до истечения старой.
	WorkingKeyRenewBefore = 3 * 24 * time.Hour

	keysFile     = "keys.json"
	nextRootFile = "next-root.key"
	storeVersion = 1
)

// KeyStore хранит идентичность установки: корневой ключ, обязательство на следующий корень и рабочие ключи.
// Файлы лежат в отдельной папке с правами 0700/0600. next-root.key администратор может вынести в резервную копию.
type KeyStore struct {
	dir string

	mu             sync.RWMutex
	installationID [16]byte
	root           ed25519.PrivateKey
	nextRoot       ed25519.PublicKey
	working        []workingKey // по возрастанию эпохи
	// lastEpoch — последняя выпущенная эпоха. Хранится отдельно: даже когда все ключи истекли,
	// новая эпоха должна быть больше всех ранее выданных, иначе клиент отвергнет её как откат.
	lastEpoch uint32
}

type workingKey struct {
	cert    WorkingCertificate
	raw     []byte
	private *ecdh.PrivateKey
}

type storedKeys struct {
	Version        int             `json:"version"`
	InstallationID []byte          `json:"installationId"`
	RootSeed       []byte          `json:"rootSeed"`
	NextRootPublic []byte          `json:"nextRootPublic"`
	LastEpoch      uint32          `json:"lastEpoch"`
	Working        []storedWorking `json:"working"`
}

type storedWorking struct {
	Private     []byte `json:"private"`
	Certificate []byte `json:"certificate"`
}

// OpenKeyStore читает ключи из dir или создаёт новую идентичность, затем выпускает рабочий ключ, если он нужен.
func OpenKeyStore(dir string, now time.Time) (*KeyStore, error) {
	if dir == "" {
		return nil, errors.New("secure transport: key directory is required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("secure transport: create key directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("secure transport: restrict key directory: %w", err)
	}
	s := &KeyStore{dir: dir}
	raw, err := os.ReadFile(filepath.Join(dir, keysFile))
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := s.create(); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, fmt.Errorf("secure transport: read keys: %w", err)
	default:
		if err := s.load(raw); err != nil {
			return nil, err
		}
	}
	if _, err := s.Rotate(now); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *KeyStore) create() error {
	if _, err := rand.Read(s.installationID[:]); err != nil {
		return err
	}
	_, root, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	nextPublic, nextPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	s.root, s.nextRoot = root, nextPublic
	// Следующий корень пишется до основного файла: обязательство не должно ссылаться на потерянный ключ.
	if err := writeFileAtomic(filepath.Join(s.dir, nextRootFile), nextPrivate.Seed()); err != nil {
		return err
	}
	return s.persistLocked()
}

func (s *KeyStore) load(raw []byte) error {
	var st storedKeys
	if err := json.Unmarshal(raw, &st); err != nil || st.Version != storeVersion ||
		len(st.InstallationID) != 16 || len(st.RootSeed) != ed25519.SeedSize || len(st.NextRootPublic) != ed25519.PublicKeySize {
		return errors.New("secure transport: keys file is corrupt or has an unknown version")
	}
	if err := os.Chmod(filepath.Join(s.dir, keysFile), 0o600); err != nil {
		return err
	}
	copy(s.installationID[:], st.InstallationID)
	s.root = ed25519.NewKeyFromSeed(st.RootSeed)
	s.nextRoot = ed25519.PublicKey(st.NextRootPublic)
	s.lastEpoch = st.LastEpoch
	if s.installationID == [16]byte{} {
		return errors.New("secure transport: keys file has a zero installation ID")
	}
	rootPublic := s.root.Public().(ed25519.PublicKey)
	for _, w := range st.Working {
		cert, err := ParseWorkingCertificate(w.Certificate, rootPublic, s.installationID)
		if err != nil {
			return fmt.Errorf("secure transport: stored working key: %w", err)
		}
		private, err := ecdh.X25519().NewPrivateKey(w.Private)
		if err != nil || [32]byte(private.PublicKey().Bytes()) != cert.WorkingKey {
			return errors.New("secure transport: stored working key does not match its certificate")
		}
		if n := len(s.working); n > 0 && s.working[n-1].cert.Epoch >= cert.Epoch {
			return errors.New("secure transport: stored working keys are not ordered by epoch")
		}
		s.working = append(s.working, workingKey{cert: cert, raw: w.Certificate, private: private})
	}
	if n := len(s.working); n > 0 && s.working[n-1].cert.Epoch > s.lastEpoch {
		return errors.New("secure transport: stored working key is newer than the last issued epoch")
	}
	return nil
}

// Rotate удаляет истёкшие рабочие ключи и выпускает следующую эпоху, когда до истечения текущей меньше WorkingKeyRenewBefore.
// Возвращает true, если набор ключей изменился. Вызывается при запуске и периодически.
func (s *KeyStore) Rotate(now time.Time) (bool, error) {
	now = now.UTC().Truncate(time.Second)
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.working[:0:0]
	for _, w := range s.working {
		if w.cert.NotAfter.After(now) {
			kept = append(kept, w)
		}
	}
	changed := len(kept) != len(s.working)
	lastEpoch := s.lastEpoch
	if n := len(kept); n == 0 || kept[n-1].cert.NotAfter.Sub(now) < WorkingKeyRenewBefore {
		if lastEpoch == ^uint32(0) {
			return false, errors.New("secure transport: working-key epochs exhausted")
		}
		lastEpoch++
		w, err := s.issueLocked(lastEpoch, now)
		if err != nil {
			return false, err
		}
		kept = append(kept, w)
		changed = true
	}
	if !changed {
		return false, nil
	}
	previous, previousEpoch := s.working, s.lastEpoch
	s.working, s.lastEpoch = kept, lastEpoch
	if err := s.persistLocked(); err != nil {
		s.working, s.lastEpoch = previous, previousEpoch
		return false, err
	}
	return true, nil
}

func (s *KeyStore) issueLocked(epoch uint32, now time.Time) (workingKey, error) {
	private, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return workingKey{}, err
	}
	cert := WorkingCertificate{
		InstallationID: s.installationID,
		Epoch:          epoch,
		NotBefore:      now,
		NotAfter:       now.Add(WorkingKeyLifetime),
		WorkingKey:     [32]byte(private.PublicKey().Bytes()),
		NextRootDigest: RootDigest(s.nextRoot),
	}
	raw, err := cert.Sign(s.root)
	if err != nil {
		return workingKey{}, err
	}
	return workingKey{cert: cert, raw: raw, private: private}, nil
}

func (s *KeyStore) persistLocked() error {
	st := storedKeys{
		Version:        storeVersion,
		InstallationID: s.installationID[:],
		RootSeed:       s.root.Seed(),
		NextRootPublic: s.nextRoot,
		LastEpoch:      s.lastEpoch,
	}
	for _, w := range s.working {
		st.Working = append(st.Working, storedWorking{Private: w.private.Bytes(), Certificate: w.raw})
	}
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(s.dir, keysFile), raw)
}

// InstallationID возвращает идентификатор установки.
func (s *KeyStore) InstallationID() [16]byte { return s.installationID }

// RootPublicKey возвращает открытый корневой ключ, который закрепляется по QR.
func (s *KeyStore) RootPublicKey() ed25519.PublicKey {
	return s.root.Public().(ed25519.PublicKey)
}

// PairingCode собирает QR v1 для внешнего адреса установки.
func (s *KeyStore) PairingCode(publicOrigin string) (string, error) {
	return PairingCode(publicOrigin, s.installationID, [32]byte(s.RootPublicKey()))
}

// CurrentCertificate возвращает сертификат новейшей эпохи — его получает клиент.
func (s *KeyStore) CurrentCertificate() []byte {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.working) == 0 {
		return nil
	}
	return append([]byte(nil), s.working[len(s.working)-1].raw...)
}

// WorkingKey возвращает закрытый ключ эпохи, если она ещё принимается в момент now.
func (s *KeyStore) WorkingKey(epoch uint32, now time.Time) (*ecdh.PrivateKey, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, w := range s.working {
		if w.cert.Epoch == epoch && now.Before(w.cert.NotAfter) && !now.Before(w.cert.NotBefore) {
			return w.private, true
		}
	}
	return nil, false
}

// writeFileAtomic пишет файл с правами 0600 через временный файл и rename, чтобы сбой не оставил половину ключей.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
