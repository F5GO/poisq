package securetransport

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// OpTicket — билет офлайн-загрузки: u8 len · id трека. Ответ: status · u16 len · токен · ключ файла [32] ·
// размер u64 · размер куска u32. Токен подставляется в GET PathPrefix+"blob/<токен>".
const OpTicket byte = 0x12

const (
	blobRatePerMinute = 120
	blobBurst         = 60
)

var errFileChanged = errors.New("secure transport: file changed since the ticket was issued")

// SetUpstream подключает обработчик прокси Studio для офлайн-загрузок (билеты и выдача файлов).
func (s *Server) SetUpstream(h http.Handler) {
	s.upstream = h
	s.blobs = newRateLimiter(blobRatePerMinute, blobBurst, maxRateEntries)
}

// plainRange читает открытые байты трека из Navidrome от имени пользователя и возвращает версию файла.
// Версия — SHA-256 от id, полного размера и Last-Modified/ETag; без них билет не выдаётся, иначе после правки
// тегов того же размера ключ и nonce повторились бы на другом содержимом.
func (s *Server) plainRange(ctx context.Context, user, password, songID string, offset, length int64) ([]byte, int64, [16]byte, error) {
	upCtx, cancel := detachedContext(ctx)
	defer cancel()
	req, err := upstreamRequest(upCtx, user, password, "stream", url.Values{"id": {songID}, "format": {"raw"}})
	if err != nil {
		return nil, 0, [16]byte{}, err
	}
	req.Header.Set("Range", "bytes="+strconv.FormatInt(offset, 10)+"-"+strconv.FormatInt(offset+length-1, 10))
	rec := &captureResponse{header: make(http.Header), limit: int(length)}
	s.upstream.ServeHTTP(rec, req)
	var total int64
	switch rec.status {
	case http.StatusPartialContent:
		total = int64(contentRangeTotal(rec.header.Get("Content-Range")))
	case http.StatusOK:
		if offset != 0 {
			return nil, 0, [16]byte{}, errors.New("secure transport: library ignored the range")
		}
		total, _ = strconv.ParseInt(rec.header.Get("Content-Length"), 10, 64)
	default:
		return nil, 0, [16]byte{}, errors.New("secure transport: library returned status " + strconv.Itoa(rec.status))
	}
	marker := rec.header.Get("Last-Modified") + "\x00" + rec.header.Get("ETag")
	if total <= 0 || marker == "\x00" || strings.HasPrefix(rec.header.Get("Content-Type"), "application/json") {
		return nil, 0, [16]byte{}, errors.New("secure transport: library response has no stable version")
	}
	h := sha256.New()
	h.Write([]byte(songID))
	_ = binary.Write(h, binary.BigEndian, total)
	h.Write([]byte(marker))
	var version [16]byte
	copy(version[:], h.Sum(nil))
	return rec.body.Bytes(), total, version, nil
}

// issueTicket выдаёт билет на трек; Navidrome проверяет право пользователя на файл первым чтением.
func (s *Server) issueTicket(ctx context.Context, sess *Session, body []byte, w io.Writer) error {
	if len(body) < 1 || int(body[0]) != len(body)-1 || body[0] == 0 {
		return writeStatus(w, StatusBadRequest)
	}
	songID := string(body[1:])
	_, _, keys := s.inst.State()
	if s.upstream == nil || keys == nil {
		return writeStatus(w, StatusUnavailable)
	}
	user, password, _ := sess.User()
	_, size, version, err := s.plainRange(ctx, user, password, songID, 0, 1)
	if err != nil {
		return writeStatus(w, StatusRangeUnsupported)
	}
	t := Ticket{DeviceID: sess.device(), SongID: songID, Version: version, Size: size, Expires: s.now().Add(TicketLifetime)}
	if _, err := rand.Read(t.Key[:]); err != nil {
		return err
	}
	token, err := sealTicket(keys.BlobKey(), t)
	if err != nil {
		return writeStatus(w, StatusBadRequest)
	}
	out := binary.BigEndian.AppendUint16([]byte{StatusOK}, uint16(len(token)))
	out = append(out, token...)
	out = append(out, t.Key[:]...)
	out = binary.BigEndian.AppendUint64(out, uint64(size))
	out = binary.BigEndian.AppendUint32(out, BlobChunk)
	_, err = w.Write(out)
	return err
}

// serveBlob отдаёт зашифрованный файл по билету. Билет проверяется до чтения библиотеки, устройство — на каждый
// запрос (отзыв устройства закрывает его билеты), версия файла — на каждое окно чтения.
func (s *Server) serveBlob(w http.ResponseWriter, r *http.Request, token string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	_, _, keys := s.inst.State()
	if s.upstream == nil || s.accounts == nil || keys == nil {
		http.NotFound(w, r)
		return
	}
	now := s.now()
	if !s.blobs.allow(s.clientIP(r), now) {
		w.WriteHeader(http.StatusTooManyRequests)
		return
	}
	t, err := openTicket(keys.BlobKey(), token, now)
	if err != nil {
		http.NotFound(w, r) // неверный, чужой и истёкший билет неотличимы
		return
	}
	user, password, err := s.accounts.devices.Credentials(r.Context(), t.DeviceID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	fetch := func(offset, length int64) ([]byte, error) {
		data, size, version, err := s.plainRange(r.Context(), user, password, t.SongID, offset, length)
		if err != nil {
			return nil, err
		}
		if size != t.Size || version != t.Version {
			return nil, errFileChanged // новое содержимое тем же ключом не шифруется
		}
		return data, nil
	}
	if _, err := fetch(0, 1); err != nil {
		if errors.Is(err, errFileChanged) {
			w.WriteHeader(http.StatusConflict)
		} else {
			w.WriteHeader(http.StatusBadGateway)
		}
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("ETag", `"`+hex.EncodeToString(t.Version[:])+`"`)
	http.ServeContent(w, r, "", time.Time{}, newBlobReader(t.Key[:], t.Size, fetch))
}

// captureResponse собирает ответ прокси в память, не больше limit байт тела.
type captureResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
	limit  int
}

func (c *captureResponse) Header() http.Header { return c.header }

func (c *captureResponse) WriteHeader(code int) {
	if c.status == 0 {
		c.status = code
	}
}

func (c *captureResponse) Write(p []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	room := c.limit - c.body.Len()
	if room <= 0 {
		return 0, errChunkFull
	}
	if len(p) > room {
		c.body.Write(p[:room])
		return room, errChunkFull
	}
	return c.body.Write(p)
}
