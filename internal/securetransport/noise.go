package securetransport

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
)

// Handshake Noise_NK_25519_AESGCM_SHA256 строго по спецификации Noise (rev. 34):
//
//	<- s
//	...
//	-> e, es
//	<- e, ee
//
// Статический ключ ответчика — рабочий ключ Studio из сертификата. Реализация проверяется официальным
// вектором (testdata/noise-nk.json); Swift реализует то же самое (NoiseNK.swift).
const noiseProtocolName = "Noise_NK_25519_AESGCM_SHA256"

const (
	noiseKeySize = 32
	noiseTagSize = 16
)

var errNoiseDecrypt = errors.New("secure transport: handshake authentication failed")

type symmetricState struct {
	ck, h [32]byte
	k     [32]byte
	hasK  bool
	n     uint64
}

func newSymmetricState(prologue []byte) *symmetricState {
	s := &symmetricState{}
	// Имя протокола короче HASHLEN: дополняется нулями, а не хэшируется.
	copy(s.h[:], noiseProtocolName)
	s.ck = s.h
	s.mixHash(prologue)
	return s
}

func (s *symmetricState) mixHash(data []byte) {
	h := sha256.New()
	h.Write(s.h[:])
	h.Write(data)
	h.Sum(s.h[:0])
}

func (s *symmetricState) mixKey(ikm []byte) {
	ck, k := noiseHKDF(s.ck[:], ikm)
	s.ck, s.k, s.hasK, s.n = ck, k, true, 0
}

func (s *symmetricState) encryptAndHash(plaintext []byte) ([]byte, error) {
	if !s.hasK {
		s.mixHash(plaintext)
		return append([]byte(nil), plaintext...), nil
	}
	c, err := sealAESGCM(s.k[:], s.n, s.h[:], plaintext)
	if err != nil {
		return nil, err
	}
	s.n++
	s.mixHash(c)
	return c, nil
}

func (s *symmetricState) decryptAndHash(ciphertext []byte) ([]byte, error) {
	if !s.hasK {
		s.mixHash(ciphertext)
		return append([]byte(nil), ciphertext...), nil
	}
	p, err := openAESGCM(s.k[:], s.n, s.h[:], ciphertext)
	if err != nil {
		return nil, errNoiseDecrypt
	}
	s.n++
	s.mixHash(ciphertext)
	return p, nil
}

// split возвращает ключи инициатор→ответчик и ответчик→инициатор.
func (s *symmetricState) split() (initiatorToResponder, responderToInitiator [32]byte) {
	return noiseHKDF(s.ck[:], nil)
}

// noiseHKDF — HKDF из спецификации Noise с двумя выходами.
func noiseHKDF(chainingKey, ikm []byte) (out1, out2 [32]byte) {
	m := hmac.New(sha256.New, chainingKey)
	m.Write(ikm)
	temp := m.Sum(nil)
	m = hmac.New(sha256.New, temp)
	m.Write([]byte{0x01})
	m.Sum(out1[:0])
	m = hmac.New(sha256.New, temp)
	m.Write(out1[:])
	m.Write([]byte{0x02})
	m.Sum(out2[:0])
	return out1, out2
}

// aesgcmNonce: 32 нулевых бита и 64-битный счётчик big-endian, как требует Noise для AESGCM.
func aesgcmNonce(n uint64) []byte {
	nonce := make([]byte, 12)
	binary.BigEndian.PutUint64(nonce[4:], n)
	return nonce
}

func newAESGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func sealAESGCM(key []byte, n uint64, ad, plaintext []byte) ([]byte, error) {
	aead, err := newAESGCM(key)
	if err != nil {
		return nil, err
	}
	return aead.Seal(nil, aesgcmNonce(n), plaintext, ad), nil
}

func openAESGCM(key []byte, n uint64, ad, ciphertext []byte) ([]byte, error) {
	aead, err := newAESGCM(key)
	if err != nil {
		return nil, err
	}
	return aead.Open(nil, aesgcmNonce(n), ciphertext, ad)
}

// noiseResult — итог handshake: полезная нагрузка первого сообщения, ключи направлений и хэш транскрипта.
type noiseResult struct {
	payload              []byte
	initiatorToResponder [32]byte
	responderToInitiator [32]byte
	handshakeHash        [32]byte
}

// respondNK обрабатывает первое сообщение и строит второе. ephemeral задаётся только в тестах; nil — случайный ключ.
func respondNK(prologue []byte, static *ecdh.PrivateKey, ephemeral *ecdh.PrivateKey, message1, payload2 []byte) (noiseResult, []byte, error) {
	if len(message1) < noiseKeySize+noiseTagSize {
		return noiseResult{}, nil, errNoiseDecrypt
	}
	s := newSymmetricState(prologue)
	s.mixHash(static.PublicKey().Bytes())

	re, err := ecdh.X25519().NewPublicKey(message1[:noiseKeySize])
	if err != nil {
		return noiseResult{}, nil, errNoiseDecrypt
	}
	s.mixHash(re.Bytes())
	es, err := static.ECDH(re) // отвергает точки малого порядка (нулевой общий секрет)
	if err != nil {
		return noiseResult{}, nil, errNoiseDecrypt
	}
	s.mixKey(es)
	payload1, err := s.decryptAndHash(message1[noiseKeySize:])
	if err != nil {
		return noiseResult{}, nil, err
	}

	if ephemeral == nil {
		if ephemeral, err = ecdh.X25519().GenerateKey(nil); err != nil {
			return noiseResult{}, nil, err
		}
	}
	message2 := append([]byte(nil), ephemeral.PublicKey().Bytes()...)
	s.mixHash(ephemeral.PublicKey().Bytes())
	ee, err := ephemeral.ECDH(re)
	if err != nil {
		return noiseResult{}, nil, errNoiseDecrypt
	}
	s.mixKey(ee)
	c, err := s.encryptAndHash(payload2)
	if err != nil {
		return noiseResult{}, nil, err
	}
	message2 = append(message2, c...)
	r := noiseResult{payload: payload1, handshakeHash: s.h}
	r.initiatorToResponder, r.responderToInitiator = s.split()
	return r, message2, nil
}

// nkInitiator — сторона клиента; в продукте её реализует Swift, в Go она нужна тестам и проверке совместимости.
type nkInitiator struct {
	s         *symmetricState
	ephemeral *ecdh.PrivateKey
}

func newNKInitiator(prologue []byte, remoteStatic *ecdh.PublicKey, ephemeral *ecdh.PrivateKey, payload1 []byte) (*nkInitiator, []byte, error) {
	var err error
	if ephemeral == nil {
		if ephemeral, err = ecdh.X25519().GenerateKey(nil); err != nil {
			return nil, nil, err
		}
	}
	s := newSymmetricState(prologue)
	s.mixHash(remoteStatic.Bytes())
	message1 := append([]byte(nil), ephemeral.PublicKey().Bytes()...)
	s.mixHash(ephemeral.PublicKey().Bytes())
	es, err := ephemeral.ECDH(remoteStatic)
	if err != nil {
		return nil, nil, err
	}
	s.mixKey(es)
	c, err := s.encryptAndHash(payload1)
	if err != nil {
		return nil, nil, err
	}
	return &nkInitiator{s: s, ephemeral: ephemeral}, append(message1, c...), nil
}

func (i *nkInitiator) readMessage2(message2 []byte) (noiseResult, error) {
	if len(message2) < noiseKeySize+noiseTagSize {
		return noiseResult{}, errNoiseDecrypt
	}
	re, err := ecdh.X25519().NewPublicKey(message2[:noiseKeySize])
	if err != nil {
		return noiseResult{}, errNoiseDecrypt
	}
	i.s.mixHash(re.Bytes())
	ee, err := i.ephemeral.ECDH(re)
	if err != nil {
		return noiseResult{}, errNoiseDecrypt
	}
	i.s.mixKey(ee)
	payload, err := i.s.decryptAndHash(message2[noiseKeySize:])
	if err != nil {
		return noiseResult{}, err
	}
	r := noiseResult{payload: payload, handshakeHash: i.s.h}
	r.initiatorToResponder, r.responderToInitiator = i.s.split()
	return r, nil
}
