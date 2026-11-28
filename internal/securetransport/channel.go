package securetransport

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
)

// Формат канала v1 (все числа big-endian). Совпадает со Swift (SecureChannel.swift).
//
// hello:    version u8 | epoch u32 | noise message 1 (e, es; пустая нагрузка)              = 53 байта
// ответ:    noise message 2 (e, ee) с нагрузкой session_id [16] | ttl u32 | maxRequest u32 | maxRecord u32
//
//	409 — тело содержит актуальный сертификат рабочего ключа.
//
// call:     version u8 | session_id [16] | counter u64 | AES-GCM(запрос)
// ответ:    последовательность записей: frame u32 | AES-GCM(часть ответа),
//
//	frame — длина шифротекста в младших 31 битах, старший бит — признак последней записи.
//
// Ключ запроса — HMAC(корень инициатор→ответчик, "poisq-secure/v1 request" || counter),
// ключ ответа — HMAC(корень ответчик→инициатор, "poisq-secure/v1 response" || counter).
// Корни из Split сами ничего не шифруют, поэтому пара ключ/nonce не повторяется между запросами.
// Nonce записи — её индекс; AAD = заголовок запроса || индекс u64 || признак последней записи u8.
const (
	// PathPrefix — префикс защищённого протокола на публичном входе.
	PathPrefix = "/poisq-secure/v1/"

	wireVersion      = 1
	helloSize        = 1 + 4 + noiseKeySize + noiseTagSize
	helloPayloadSize = 16 + 4 + 4 + 4
	callHeaderSize   = 1 + 16 + 8
	// MaxRequestSize ограничивает расшифрованный запрос; тело запроса проверяется до расшифровки.
	MaxRequestSize = 64 << 10
	// MaxRecordSize — открытый размер записи ответа (начальное значение по испытаниям на CDN).
	MaxRecordSize = 16 << 10

	prologueLabel = "poisq-secure/v1"
	requestLabel  = "poisq-secure/v1 request"
	responseLabel = "poisq-secure/v1 response"
)

var errRecord = errors.New("secure transport: record authentication failed")

const finalRecordBit = 1 << 31

func handshakePrologue(installationID [16]byte, epoch uint32) []byte {
	p := append([]byte(prologueLabel), installationID[:]...)
	return binary.BigEndian.AppendUint32(p, epoch)
}

func deriveKey(root [32]byte, label string, counter uint64) []byte {
	m := hmac.New(sha256.New, root[:])
	m.Write([]byte(label))
	var c [8]byte
	binary.BigEndian.PutUint64(c[:], counter)
	m.Write(c[:])
	return m.Sum(nil)
}

func recordAAD(header []byte, index uint64, final bool) []byte {
	aad := append(append([]byte(nil), header...), make([]byte, 9)...)
	binary.BigEndian.PutUint64(aad[len(header):], index)
	if final {
		aad[len(aad)-1] = 1
	}
	return aad
}

func callHeader(sessionID [16]byte, counter uint64) []byte {
	h := make([]byte, 0, callHeaderSize)
	h = append(h, wireVersion)
	h = append(h, sessionID[:]...)
	return binary.BigEndian.AppendUint64(h, counter)
}

// recordWriter шифрует ответ записями до MaxRecordSize; Close дописывает последнюю запись.
// Без последней записи клиент считает ответ усечённым.
type recordWriter struct {
	w      io.Writer
	flush  func()
	key    []byte
	header []byte
	index  uint64
	buf    []byte
	closed bool
	err    error
}

func newRecordWriter(w io.Writer, flush func(), key, header []byte) *recordWriter {
	return &recordWriter{w: w, flush: flush, key: key, header: header, buf: make([]byte, 0, MaxRecordSize)}
}

func (rw *recordWriter) Write(p []byte) (int, error) {
	if rw.closed {
		return 0, errors.New("secure transport: write after close")
	}
	written := 0
	for len(p) > 0 && rw.err == nil {
		n := min(MaxRecordSize-len(rw.buf), len(p))
		rw.buf = append(rw.buf, p[:n]...)
		p, written = p[n:], written+n
		if len(rw.buf) == MaxRecordSize {
			rw.emit(false)
		}
	}
	return written, rw.err
}

func (rw *recordWriter) Close() error {
	if rw.closed {
		return rw.err
	}
	rw.closed = true
	rw.emit(true)
	return rw.err
}

func (rw *recordWriter) emit(final bool) {
	if rw.err != nil {
		return
	}
	c, err := sealAESGCM(rw.key, rw.index, recordAAD(rw.header, rw.index, final), rw.buf)
	if err != nil {
		rw.err = err
		return
	}
	header := uint32(len(c))
	if final {
		header |= finalRecordBit
	}
	frame := binary.BigEndian.AppendUint32(make([]byte, 0, 4+len(c)), header)
	if _, err := rw.w.Write(append(frame, c...)); err != nil {
		rw.err = err
		return
	}
	rw.index++
	rw.buf = rw.buf[:0]
	if rw.flush != nil {
		rw.flush()
	}
}

// readRecords расшифровывает ответ целиком, проверяя порядок, размер записей и наличие последней записи.
// Используется тестами и Go-клиентом проверки совместимости; Swift читает записи потоком.
func readRecords(r io.Reader, key, header []byte, limit int) ([]byte, error) {
	var out []byte
	for index := uint64(0); ; index++ {
		var frame [4]byte
		if _, err := io.ReadFull(r, frame[:]); err != nil {
			return nil, errRecord
		}
		v := binary.BigEndian.Uint32(frame[:])
		final, n := v&finalRecordBit != 0, v&^finalRecordBit
		if n < noiseTagSize || n > MaxRecordSize+noiseTagSize {
			return nil, errRecord
		}
		c := make([]byte, n)
		if _, err := io.ReadFull(r, c); err != nil {
			return nil, errRecord
		}
		p, err := openAESGCM(key, index, recordAAD(header, index, final), c)
		if err != nil || len(out)+len(p) > limit {
			return nil, errRecord
		}
		out = append(out, p...)
		if final {
			return out, nil
		}
	}
}

// replayWindow принимает каждый счётчик не больше одного раза в окне из 4096 значений (как в IPsec, RFC 6479).
type replayWindow struct {
	max  uint64
	bits [replayWindowSize / 64]uint64
}

const replayWindowSize = 4096

func (w *replayWindow) accept(counter uint64) bool {
	if counter == 0 {
		return false
	}
	if counter > w.max {
		if counter-w.max >= replayWindowSize {
			w.bits = [replayWindowSize / 64]uint64{}
		} else {
			for c := w.max + 1; c < counter; c++ {
				w.clear(c)
			}
		}
		w.max = counter
		w.set(counter)
		return true
	}
	if w.max-counter >= replayWindowSize || w.test(counter) {
		return false
	}
	w.set(counter)
	return true
}

func (w *replayWindow) pos(c uint64) (int, uint64) {
	i := c % replayWindowSize
	return int(i / 64), 1 << (i % 64)
}
func (w *replayWindow) set(c uint64)       { i, m := w.pos(c); w.bits[i] |= m }
func (w *replayWindow) clear(c uint64)     { i, m := w.pos(c); w.bits[i] &^= m }
func (w *replayWindow) test(c uint64) bool { i, m := w.pos(c); return w.bits[i]&m != 0 }
