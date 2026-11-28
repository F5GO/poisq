package securetransport

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"time"
)

// Офлайн-загрузка: файл шифруется на лету кусками по BlobChunk открытых байт (AES-256-GCM).
// Кусок i: nonce — 32 нулевых бита и i (u64), AAD — blobLabel ‖ i u64 ‖ число кусков u64 ‖ признак последнего u8.
// Шифротекст куска — BlobChunk+16 байт (последний короче), поэтому любой диапазон байт вычисляется заново
// одинаково, и докачка через Range работает без хранения шифротекста. Ключ файла случаен для каждого билета.
// Формат совпадает со Swift (SecureBlob.swift) и закреплён вектором testdata/blob-v1.json.
const (
	BlobChunk = 64 << 10
	// TicketLifetime — срок билета: задача фоновой загрузки может ждать Wi-Fi часами.
	TicketLifetime = 7 * 24 * time.Hour

	blobCipherChunk = BlobChunk + noiseTagSize
	blobWindow      = 16 // кусков за одно обращение к Navidrome (1 МиБ)
	blobLabel       = "poisq-secure/v1 blob"
	ticketLabel     = "poisq-secure/v1 ticket"
	ticketVersion   = 1
	maxSongID       = 255
)

var errTicket = errors.New("secure transport: invalid download ticket")

func blobChunks(size int64) int64 {
	if size <= 0 {
		return 1 // пустой файл — один пустой последний кусок
	}
	return (size + BlobChunk - 1) / BlobChunk
}

// BlobCipherSize — размер зашифрованного файла с открытым размером size.
func BlobCipherSize(size int64) int64 { return size + noiseTagSize*blobChunks(size) }

func blobAAD(index, total uint64, final bool) []byte {
	aad := append([]byte(blobLabel), make([]byte, 17)...)
	binary.BigEndian.PutUint64(aad[len(blobLabel):], index)
	binary.BigEndian.PutUint64(aad[len(blobLabel)+8:], total)
	if final {
		aad[len(aad)-1] = 1
	}
	return aad
}

func sealBlobChunk(key []byte, index, total uint64, plain []byte) ([]byte, error) {
	return sealAESGCM(key, index, blobAAD(index, total, index == total-1), plain)
}

// Ticket — право на загрузку одного файла одним устройством. Запечатан blobKey установки:
// сам токен в адресе даёт только шифротекст, ключ файла клиент получает внутри канала.
type Ticket struct {
	DeviceID string // 32 hex
	SongID   string
	Version  [16]byte // версия файла: изменение файла делает билет недействительным
	Size     int64
	Expires  time.Time
	Key      [32]byte
}

// sealTicket: версия u8 · nonce [12] · AES-GCM(blobKey, устройство [16] · версия [16] · размер u64 · срок i64 · ключ [32] · u8 len · id).
func sealTicket(blobKey [32]byte, t Ticket) (string, error) {
	device, err := hex.DecodeString(t.DeviceID)
	if err != nil || len(device) != 16 || len(t.SongID) == 0 || len(t.SongID) > maxSongID || t.Size < 0 {
		return "", errTicket
	}
	plain := append([]byte(nil), device...)
	plain = append(plain, t.Version[:]...)
	plain = binary.BigEndian.AppendUint64(plain, uint64(t.Size))
	plain = binary.BigEndian.AppendUint64(plain, uint64(t.Expires.Unix()))
	plain = append(plain, t.Key[:]...)
	plain = append(append(plain, byte(len(t.SongID))), t.SongID...)
	aead, err := newAESGCM(blobKey[:])
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := append([]byte{ticketVersion}, nonce...)
	out = aead.Seal(out, nonce, plain, []byte(ticketLabel))
	return base64.RawURLEncoding.EncodeToString(out), nil
}

func openTicket(blobKey [32]byte, token string, now time.Time) (Ticket, error) {
	if len(token) > 1024 {
		return Ticket{}, errTicket
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) < 1+12+noiseTagSize || raw[0] != ticketVersion {
		return Ticket{}, errTicket
	}
	aead, err := newAESGCM(blobKey[:])
	if err != nil {
		return Ticket{}, err
	}
	plain, err := aead.Open(nil, raw[1:13], raw[13:], []byte(ticketLabel))
	if err != nil || len(plain) < 16+16+8+8+32+1 {
		return Ticket{}, errTicket
	}
	var t Ticket
	t.DeviceID = hex.EncodeToString(plain[:16])
	copy(t.Version[:], plain[16:32])
	t.Size = int64(binary.BigEndian.Uint64(plain[32:40]))
	t.Expires = time.Unix(int64(binary.BigEndian.Uint64(plain[40:48])), 0)
	copy(t.Key[:], plain[48:80])
	n := int(plain[80])
	if len(plain) != 81+n || n == 0 || t.Size < 0 {
		return Ticket{}, errTicket
	}
	t.SongID = string(plain[81:])
	if !now.Before(t.Expires) {
		return Ticket{}, errTicket
	}
	return t, nil
}

// blobReader — зашифрованный файл как io.ReadSeeker для http.ServeContent (Range, If-Range).
// Открытые байты читаются окнами через fetch; fetch обязан проверить, что файл не изменился.
type blobReader struct {
	key   []byte
	size  int64
	total uint64
	fetch func(offset, length int64) ([]byte, error)

	pos      int64
	winIndex int64
	win      []byte
}

func newBlobReader(key []byte, size int64, fetch func(offset, length int64) ([]byte, error)) *blobReader {
	return &blobReader{key: key, size: size, total: uint64(blobChunks(size)), fetch: fetch, winIndex: -1}
}

func (r *blobReader) Seek(offset int64, whence int) (int64, error) {
	var next int64
	switch whence {
	case io.SeekStart:
		next = offset
	case io.SeekCurrent:
		next = r.pos + offset
	case io.SeekEnd:
		next = BlobCipherSize(r.size) + offset
	default:
		return 0, errors.New("secure transport: invalid whence")
	}
	if next < 0 {
		return 0, errors.New("secure transport: negative position")
	}
	r.pos = next
	return next, nil
}

func (r *blobReader) Read(p []byte) (int, error) {
	if r.pos >= BlobCipherSize(r.size) {
		return 0, io.EOF
	}
	chunk := r.pos / blobCipherChunk
	w := chunk / blobWindow
	if w != r.winIndex {
		if err := r.load(w); err != nil {
			return 0, err
		}
	}
	start := w * blobWindow * blobCipherChunk
	n := copy(p, r.win[r.pos-start:])
	r.pos += int64(n)
	return n, nil
}

func (r *blobReader) load(w int64) error {
	first := w * blobWindow
	last := min(first+blobWindow, int64(r.total))
	plainOff := first * BlobChunk
	plainLen := min(int64(blobWindow)*BlobChunk, r.size-plainOff)
	var plain []byte
	if plainLen > 0 {
		var err error
		if plain, err = r.fetch(plainOff, plainLen); err != nil {
			return err
		}
		if int64(len(plain)) != plainLen {
			return errors.New("secure transport: short read from library")
		}
	}
	win := make([]byte, 0, (last-first)*blobCipherChunk)
	for i := first; i < last; i++ {
		lo := (i - first) * BlobChunk
		hi := min(lo+BlobChunk, int64(len(plain)))
		c, err := sealBlobChunk(r.key, uint64(i), r.total, plain[lo:hi])
		if err != nil {
			return err
		}
		win = append(win, c...)
	}
	r.win, r.winIndex = win, w
	return nil
}
