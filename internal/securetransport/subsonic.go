package securetransport

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// OpSubsonic — запрос Subsonic внутри канала: u8 len · метод · u32 len · параметры (application/x-www-form-urlencoded).
// Ответ: status · HTTP-статус u16 · u8 len · Content-Type · тело. Авторизацию добавляет Studio от имени пользователя сессии.
const OpSubsonic byte = 0x10

// OpMedia — диапазон байт исходного аудио: u8 len · метод (stream|download) · u32 len · параметры · offset u64 · length u32.
// Ответ: status · HTTP-статус u16 · полный размер u64 (0 — неизвестен) · байты. Перекодирование не используется:
// ответ без поддержки диапазонов при ненулевом смещении отклоняется статусом StatusRangeUnsupported.
const OpMedia byte = 0x11

// MaxMediaChunk ограничивает один запрос диапазона.
const MaxMediaChunk = 8 << 20

// StatusRangeUnsupported — источник не поддерживает чтение с произвольного места.
const StatusRangeUnsupported byte = 0x09

var mediaMethods = map[string]bool{"stream": true, "download": true}

var errChunkFull = errors.New("secure transport: media chunk complete")

// allowedSubsonicMethods — явный список методов, доступных через канал. Административные методы, управление
// пользователями и публичные ссылки (shares) закрыты: им нужна отдельная проверка маршрута и прав.
var allowedSubsonicMethods = map[string]bool{
	"ping": true, "getLicense": true, "getOpenSubsonicExtensions": true, "getMusicFolders": true, "getUser": true,
	"getIndexes": true, "getMusicDirectory": true, "getGenres": true, "getArtists": true, "getArtist": true,
	"getAlbum": true, "getSong": true, "getArtistInfo": true, "getArtistInfo2": true, "getAlbumInfo": true,
	"getAlbumInfo2": true, "getSimilarSongs": true, "getSimilarSongs2": true, "getTopSongs": true,
	"getAlbumList": true, "getAlbumList2": true, "getRandomSongs": true, "getSongsByGenre": true,
	"getNowPlaying": true, "getStarred": true, "getStarred2": true, "search2": true, "search3": true,
	"getPlaylists": true, "getPlaylist": true, "createPlaylist": true, "updatePlaylist": true, "deletePlaylist": true,
	"stream": true, "download": true, "getCoverArt": true, "getLyrics": true, "getLyricsBySongId": true,
	"star": true, "unstar": true, "setRating": true, "scrobble": true,
	"getBookmarks": true, "createBookmark": true, "deleteBookmark": true, "getPlayQueue": true, "savePlayQueue": true,
	"getPodcasts": true, "getNewestPodcasts": true, "refreshPodcasts": true, "createPodcastChannel": true,
	"deletePodcastChannel": true, "deletePodcastEpisode": true, "downloadPodcastEpisode": true,
	"getInternetRadioStations": true, "createInternetRadioStation": true, "updateInternetRadioStation": true,
	"deleteInternetRadioStation": true, "getScanStatus": true,
	"poisqCastZones": true, "poisqCastPlay": true, "poisqCastControl": true, "poisqCastStatus": true,
}

// authParams — параметры авторизации, которые клиент не может задать сам.
var subsonicAuthParams = []string{"u", "t", "s", "p", "apiKey", "jwt"}

// NewSubsonicHandler передаёт разрешённые Subsonic-запросы в upstream (обработчик прокси Studio) внутри процесса.
func NewSubsonicHandler(upstream http.Handler) Handler {
	return func(ctx context.Context, sess *Session, request []byte, w io.Writer) error {
		if request[0] == OpMedia {
			return serveMedia(ctx, upstream, sess, request[1:], w)
		}
		if request[0] != OpSubsonic {
			return writeStatus(w, StatusUnknownOp)
		}
		method, query, ok := parseSubsonicRequest(request[1:])
		if !ok {
			return writeStatus(w, StatusBadRequest)
		}
		if !allowedSubsonicMethods[method] {
			return writeStatus(w, StatusForbidden)
		}
		upCtx, cancel := detachedContext(ctx)
		defer cancel()
		user, password, _ := sess.User()
		req, err := upstreamRequest(upCtx, user, password, method, query)
		if err != nil {
			return writeStatus(w, StatusBadRequest)
		}
		rw := &channelResponse{w: w, header: make(http.Header)}
		upstream.ServeHTTP(rw, req)
		if !rw.wrote {
			rw.WriteHeader(http.StatusOK)
		}
		return rw.err
	}
}

type channelOriginKey struct{}

// IsChannelRequest сообщает, что внутренний запрос к прокси пришёл из защищённого канала. Метку ставит только
// securetransport в контексте; внешний клиент не может её подделать. По ней прокси не применяет к каналу
// политику «только защищённый вход».
func IsChannelRequest(ctx context.Context) bool {
	v, _ := ctx.Value(channelOriginKey{}).(bool)
	return v
}

// ChannelContext помечает контекст внутреннего запроса как пришедший из канала.
func ChannelContext(parent context.Context) context.Context {
	return context.WithValue(parent, channelOriginKey{}, true)
}

// detachedContext отменяется вместе с parent, но не несёт его значений. Внутри HTTP-сервера httputil.ReverseProxy
// на ошибке копирования вызывает panic(http.ErrAbortHandler) и обрывает соединение вместе с зашифрованным ответом;
// без http.ServerContextKey он только пишет в журнал, а канал завершает ответ сам.
func detachedContext(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ChannelContext(context.Background()))
	stop := context.AfterFunc(parent, cancel)
	return ctx, func() { stop(); cancel() }
}

// upstreamRequest строит внутренний запрос к прокси от имени пользователя сессии: клиентская авторизация отбрасывается.
func upstreamRequest(ctx context.Context, user, password, method string, query url.Values) (*http.Request, error) {
	for _, k := range subsonicAuthParams {
		query.Del(k)
	}
	var saltBytes [8]byte
	if _, err := rand.Read(saltBytes[:]); err != nil {
		return nil, err
	}
	salt := hex.EncodeToString(saltBytes[:])
	sum := md5.Sum([]byte(password + salt))
	query.Set("u", user)
	query.Set("t", hex.EncodeToString(sum[:]))
	query.Set("s", salt)
	if query.Get("v") == "" {
		query.Set("v", "1.16.1")
	}
	if query.Get("c") == "" {
		query.Set("c", "poisq")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "/rest/"+method+"?"+query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Host = "localhost"
	// Непарсируемый адрес: прокси считает источник недоверенным и не принимает заголовков о клиенте.
	req.RemoteAddr = "secure-transport"
	return req, nil
}

// serveMedia читает диапазон исходного аудио. Тело ограничено запрошенной длиной: лишнее upstream не дочитывается.
func serveMedia(ctx context.Context, upstream http.Handler, sess *Session, body []byte, w io.Writer) error {
	if len(body) < 12 {
		return writeStatus(w, StatusBadRequest)
	}
	method, query, ok := parseSubsonicRequest(body[:len(body)-12])
	if !ok {
		return writeStatus(w, StatusBadRequest)
	}
	offset := binary.BigEndian.Uint64(body[len(body)-12:])
	length := binary.BigEndian.Uint32(body[len(body)-4:])
	if !mediaMethods[method] {
		return writeStatus(w, StatusForbidden)
	}
	if length == 0 || length > MaxMediaChunk || offset > 1<<50 {
		return writeStatus(w, StatusBadRequest)
	}
	if method == "stream" {
		// Только исходные байты: перекодированный поток не читается с произвольного места.
		query.Del("maxBitRate")
		query.Set("format", "raw")
		query.Del("timeOffset")
	}
	upCtx, cancel := detachedContext(ctx)
	defer cancel()
	user, password, _ := sess.User()
	req, err := upstreamRequest(upCtx, user, password, method, query)
	if err != nil {
		return writeStatus(w, StatusBadRequest)
	}
	req.Header.Set("Range", "bytes="+strconv.FormatUint(offset, 10)+"-"+strconv.FormatUint(offset+uint64(length)-1, 10))
	rw := &mediaResponse{w: w, header: make(http.Header), offset: offset, remaining: int(length)}
	upstream.ServeHTTP(rw, req)
	if !rw.wrote {
		rw.WriteHeader(http.StatusOK)
	}
	if errors.Is(rw.err, errChunkFull) {
		return nil
	}
	return rw.err
}

// mediaResponse пишет заголовок диапазона и не больше remaining байт тела.
type mediaResponse struct {
	w         io.Writer
	header    http.Header
	offset    uint64
	remaining int
	wrote     bool
	skip      bool
	err       error
}

func (r *mediaResponse) Header() http.Header { return r.header }

func (r *mediaResponse) WriteHeader(code int) {
	if r.wrote {
		return
	}
	r.wrote = true
	var total uint64
	switch {
	case code == http.StatusPartialContent:
		total = contentRangeTotal(r.header.Get("Content-Range"))
	case code == http.StatusOK && r.offset == 0:
		total, _ = strconv.ParseUint(r.header.Get("Content-Length"), 10, 64)
	case code == http.StatusOK:
		// Источник проигнорировал Range: эти байты не с того места.
		r.skip = true
		_, r.err = r.w.Write([]byte{StatusRangeUnsupported})
		return
	}
	prefix := binary.BigEndian.AppendUint16([]byte{StatusOK}, uint16(code))
	prefix = binary.BigEndian.AppendUint64(prefix, total)
	_, r.err = r.w.Write(prefix)
}

func (r *mediaResponse) Write(p []byte) (int, error) {
	if !r.wrote {
		r.WriteHeader(http.StatusOK)
	}
	if r.err != nil {
		return 0, r.err
	}
	if r.skip {
		r.err = errChunkFull
		return 0, r.err
	}
	if len(p) > 0 && r.remaining == 0 {
		r.err = errChunkFull // источник прислал больше запрошенного: дальше не копируем
		return 0, r.err
	}
	n := min(len(p), r.remaining)
	if _, err := r.w.Write(p[:n]); err != nil {
		r.err = err
		return 0, err
	}
	r.remaining -= n
	if n < len(p) {
		r.err = errChunkFull
		return n, r.err
	}
	return n, nil
}

// contentRangeTotal разбирает «bytes a-b/total»; неизвестный размер — 0.
func contentRangeTotal(v string) uint64 {
	_, after, ok := strings.Cut(v, "/")
	if !ok {
		return 0
	}
	total, err := strconv.ParseUint(strings.TrimSpace(after), 10, 64)
	if err != nil {
		return 0
	}
	return total
}

func parseSubsonicRequest(b []byte) (string, url.Values, bool) {
	if len(b) < 1 {
		return "", nil, false
	}
	n := int(b[0])
	b = b[1:]
	if n == 0 || len(b) < n+4 {
		return "", nil, false
	}
	method := string(b[:n])
	b = b[n:]
	for i := 0; i < len(method); i++ {
		c := method[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return "", nil, false
		}
	}
	qn := int(binary.BigEndian.Uint32(b))
	b = b[4:]
	if qn != len(b) {
		return "", nil, false
	}
	query, err := url.ParseQuery(string(b))
	if err != nil {
		return "", nil, false
	}
	return method, query, true
}

// channelResponse пишет ответ прокси в канал: первым идёт заголовок со статусом и типом содержимого.
type channelResponse struct {
	w      io.Writer
	header http.Header
	wrote  bool
	err    error
}

func (r *channelResponse) Header() http.Header { return r.header }

func (r *channelResponse) WriteHeader(code int) {
	if r.wrote {
		return
	}
	r.wrote = true
	ct := r.header.Get("Content-Type")
	if len(ct) > 255 {
		ct = ct[:255]
	}
	prefix := binary.BigEndian.AppendUint16([]byte{StatusOK}, uint16(code))
	prefix = append(append(prefix, byte(len(ct))), ct...)
	_, r.err = r.w.Write(prefix)
}

func (r *channelResponse) Write(p []byte) (int, error) {
	if !r.wrote {
		r.WriteHeader(http.StatusOK)
	}
	if r.err != nil {
		return 0, r.err
	}
	n, err := r.w.Write(p)
	if err != nil {
		r.err = err
	}
	return n, err
}
