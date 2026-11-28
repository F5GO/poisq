package securetransport

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// Сертификат рабочего ключа v1 — фиксированная бинарная запись, подписанная корневым ключом Ed25519:
//
//	version u8 | installation_id [16] | epoch u32 BE | not_before i64 BE | not_after i64 BE |
//	working_key [32] (X25519) | next_root_digest [32] (SHA-256) | signature [64]
//
// Подписывается certificateContext || всё до подписи. Формат совпадает со Swift (SecureWorkingCertificate).
const (
	certificateVersion  = 1
	certificateBodySize = 1 + 16 + 4 + 8 + 8 + 32 + 32
	// CertificateSize — длина сертификата вместе с подписью.
	CertificateSize = certificateBodySize + ed25519.SignatureSize
	// MaxCertificateLifetime ограничивает ущерб от выпущенного по ошибке или украденного рабочего ключа.
	MaxCertificateLifetime = 8 * 24 * time.Hour
	// ClockSkew — допуск на расхождение часов клиента при проверке сроков.
	ClockSkew = time.Hour

	certificateContext = "poisq-secure/v1 working-key certificate"
)

// WorkingCertificate описывает рабочий ключ X25519 одной эпохи.
type WorkingCertificate struct {
	InstallationID [16]byte
	Epoch          uint32
	NotBefore      time.Time
	NotAfter       time.Time
	WorkingKey     [32]byte
	NextRootDigest [32]byte
}

var errInvalidCertificate = errors.New("secure transport: invalid working-key certificate")

// Sign кодирует и подписывает сертификат корневым ключом.
func (c WorkingCertificate) Sign(root ed25519.PrivateKey) ([]byte, error) {
	if len(root) != ed25519.PrivateKeySize || !c.wellFormed() {
		return nil, errInvalidCertificate
	}
	body := c.body()
	return append(body, ed25519.Sign(root, signedMessage(body))...), nil
}

// ParseWorkingCertificate проверяет структуру, установку и подпись. Сроки проверяет ValidAt.
func ParseWorkingCertificate(raw []byte, root ed25519.PublicKey, installationID [16]byte) (WorkingCertificate, error) {
	if len(raw) != CertificateSize || raw[0] != certificateVersion || len(root) != ed25519.PublicKeySize {
		return WorkingCertificate{}, errInvalidCertificate
	}
	body := raw[:certificateBodySize]
	if !ed25519.Verify(root, signedMessage(body), raw[certificateBodySize:]) {
		return WorkingCertificate{}, errInvalidCertificate
	}
	var c WorkingCertificate
	copy(c.InstallationID[:], body[1:17])
	c.Epoch = binary.BigEndian.Uint32(body[17:21])
	c.NotBefore = time.Unix(int64(binary.BigEndian.Uint64(body[21:29])), 0).UTC()
	c.NotAfter = time.Unix(int64(binary.BigEndian.Uint64(body[29:37])), 0).UTC()
	copy(c.WorkingKey[:], body[37:69])
	copy(c.NextRootDigest[:], body[69:101])
	if subtle.ConstantTimeCompare(c.InstallationID[:], installationID[:]) != 1 || !c.wellFormed() {
		return WorkingCertificate{}, errInvalidCertificate
	}
	return c, nil
}

// ValidAt сообщает, действует ли сертификат в момент now с учётом ClockSkew.
func (c WorkingCertificate) ValidAt(now time.Time) bool {
	return !now.Add(ClockSkew).Before(c.NotBefore) && now.Add(-ClockSkew).Before(c.NotAfter)
}

func (c WorkingCertificate) wellFormed() bool {
	lifetime := c.NotAfter.Sub(c.NotBefore)
	return c.InstallationID != [16]byte{} && c.Epoch != 0 && c.WorkingKey != [32]byte{} &&
		c.NextRootDigest != [32]byte{} && c.NotBefore.Unix() > 0 && lifetime > 0 && lifetime <= MaxCertificateLifetime
}

func (c WorkingCertificate) body() []byte {
	b := make([]byte, 0, certificateBodySize)
	b = append(b, certificateVersion)
	b = append(b, c.InstallationID[:]...)
	b = binary.BigEndian.AppendUint32(b, c.Epoch)
	b = binary.BigEndian.AppendUint64(b, uint64(c.NotBefore.Unix()))
	b = binary.BigEndian.AppendUint64(b, uint64(c.NotAfter.Unix()))
	b = append(b, c.WorkingKey[:]...)
	return append(b, c.NextRootDigest[:]...)
}

func signedMessage(body []byte) []byte {
	return append([]byte(certificateContext), body...)
}

// RootDigest — обязательство на следующий корневой ключ, публикуемое в сертификатах.
func RootDigest(root ed25519.PublicKey) [32]byte { return sha256.Sum256(root) }

// KeyFingerprint — короткий отпечаток корневого ключа для сверки человеком: первые 10 байт SHA-256
// в пяти группах по четыре шестнадцатеричных символа. Совпадает со Swift.
func KeyFingerprint(root ed25519.PublicKey) string {
	sum := sha256.Sum256(root)
	h := hex.EncodeToString(sum[:10])
	groups := make([]string, 0, 5)
	for i := 0; i < len(h); i += 4 {
		groups = append(groups, h[i:i+4])
	}
	return strings.Join(groups, " ")
}
