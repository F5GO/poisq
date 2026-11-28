package securetransport

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"io"
	"net/http"
	"net/url"
)

// OpSubsonic — запрос Subsonic внутри канала: u8 len · метод · u32 len · параметры (application/x-www-form-urlencoded).
// Ответ: status · HTTP-статус u16 · u8 len · Content-Type · тело. Авторизацию добавляет Studio от имени пользователя сессии.
const OpSubsonic byte = 0x10

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
		user, password, _ := sess.User()
		for _, k := range subsonicAuthParams {
			query.Del(k)
		}
		var saltBytes [8]byte
		if _, err := rand.Read(saltBytes[:]); err != nil {
			return err
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
			return writeStatus(w, StatusBadRequest)
		}
		req.Host = "localhost"
		// Непарсируемый адрес: прокси считает источник недоверенным и не принимает заголовков о клиенте.
		req.RemoteAddr = "secure-transport"
		rw := &channelResponse{w: w, header: make(http.Header)}
		upstream.ServeHTTP(rw, req)
		if !rw.wrote {
			rw.WriteHeader(http.StatusOK)
		}
		return rw.err
	}
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
