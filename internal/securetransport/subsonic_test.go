package securetransport

import (
	"bytes"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func subsonicRequest(method, query string) []byte {
	b := append([]byte{OpSubsonic, byte(len(method))}, method...)
	b = binary.BigEndian.AppendUint32(b, uint32(len(query)))
	return append(b, query...)
}

type subsonicReply struct {
	status      byte
	httpStatus  int
	contentType string
	body        []byte
}

func parseSubsonicReply(t *testing.T, resp []byte) subsonicReply {
	t.Helper()
	if len(resp) == 1 {
		return subsonicReply{status: resp[0]}
	}
	if len(resp) < 4 || resp[0] != StatusOK {
		t.Fatalf("reply %x", resp)
	}
	n := int(resp[3])
	return subsonicReply{status: resp[0], httpStatus: int(binary.BigEndian.Uint16(resp[1:3])), contentType: string(resp[4 : 4+n]), body: resp[4+n:]}
}

func TestSubsonicThroughChannel(t *testing.T) {
	var got *http.Request
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		if r.URL.Path == "/rest/getCoverArt" {
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(bytes.Repeat([]byte{0xff}, 40000))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"subsonic-response":{"status":"ok"}}`))
	})
	f := newAccountsFixture(t)
	f.srv.handler = NewSubsonicHandler(upstream)
	c := f.client(t)

	if _, resp, _ := c.call(subsonicRequest("getArtists", "f=json")); !bytes.Equal(resp, []byte{StatusForbidden}) {
		t.Fatalf("Subsonic before sign-in: %x", resp)
	}
	if _, resp, _ := c.call(loginRequest("alice", "correct horse", "x")); resp[0] != StatusOK {
		t.Fatal("login failed")
	}

	// Клиентские параметры авторизации отбрасываются: Studio подставляет пользователя сессии.
	_, resp, err := c.call(subsonicRequest("getArtists", "f=json&musicFolderId=2&u=mallory&p=x&t=y&s=z&apiKey=k"))
	if err != nil {
		t.Fatal(err)
	}
	r := parseSubsonicReply(t, resp)
	if r.httpStatus != 200 || r.contentType != "application/json" || !strings.Contains(string(r.body), `"ok"`) {
		t.Fatalf("reply %+v", r)
	}
	q := got.URL.Query()
	sum := md5.Sum([]byte("correct horse" + q.Get("s")))
	if got.URL.Path != "/rest/getArtists" || q.Get("u") != "alice" || q.Get("t") != hex.EncodeToString(sum[:]) ||
		q.Get("p") != "" || q.Get("apiKey") != "" || q.Get("musicFolderId") != "2" || q.Get("f") != "json" || q.Get("v") == "" {
		t.Fatalf("upstream query %v", q)
	}
	if got.RemoteAddr != "secure-transport" || got.Header.Get("X-Forwarded-For") != "" {
		t.Fatalf("upstream request must look untrusted: %q", got.RemoteAddr)
	}

	_, resp, err = c.call(subsonicRequest("getCoverArt", "id=al-1&size=600"))
	if r := parseSubsonicReply(t, resp); err != nil || r.contentType != "image/jpeg" || len(r.body) != 40000 {
		t.Fatalf("cover through several records: %v %+v", err, r.contentType)
	}

	for _, method := range []string{"createUser", "deleteUser", "changePassword", "getUsers", "createShare", "startScan", "jukeboxControl", "getArtists.view"} {
		if _, resp, _ := c.call(subsonicRequest(method, "")); len(resp) != 1 || resp[0] == StatusOK {
			t.Errorf("%s: %x", method, resp)
		}
	}
	for name, body := range map[string][]byte{
		"path traversal": subsonicRequest("../admin", ""),
		"empty method":   subsonicRequest("", ""),
		"bad length":     append(subsonicRequest("ping", "a=1"), 'x'),
		"bad query":      subsonicRequest("ping", "%zz"),
	} {
		if _, resp, _ := c.call(body); !bytes.Equal(resp, []byte{StatusBadRequest}) {
			t.Errorf("%s: %x", name, resp)
		}
	}
}

func mediaRequest(method, query string, offset uint64, length uint32) []byte {
	b := subsonicRequest(method, query)
	b[0] = OpMedia
	b = binary.BigEndian.AppendUint64(b, offset)
	return binary.BigEndian.AppendUint32(b, length)
}

func parseMediaReply(t *testing.T, resp []byte) (byte, int, uint64, []byte) {
	t.Helper()
	if len(resp) == 1 {
		return resp[0], 0, 0, nil
	}
	if len(resp) < 11 || resp[0] != StatusOK {
		t.Fatalf("media reply %x", resp[:min(len(resp), 16)])
	}
	return resp[0], int(binary.BigEndian.Uint16(resp[1:3])), binary.BigEndian.Uint64(resp[3:11]), resp[11:]
}

func TestMediaRangesThroughChannel(t *testing.T) {
	track := make([]byte, 10<<20+123)
	for i := range track {
		track[i] = byte(i*7 + i>>11)
	}
	var lastQuery url.Values
	var ignoreRange bool
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastQuery = r.URL.Query()
		if r.URL.Query().Get("id") == "missing" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"subsonic-response":{"status":"failed"}}`))
			return
		}
		if ignoreRange {
			r.Header.Del("Range")
		}
		w.Header().Set("Content-Type", "audio/flac")
		http.ServeContent(w, r, "t.flac", time.Time{}, bytes.NewReader(track))
	})
	f := newAccountsFixture(t)
	f.srv.handler = NewSubsonicHandler(upstream)
	c := f.client(t)
	if _, resp, _ := c.call(mediaRequest("stream", "id=1", 0, 1024)); !bytes.Equal(resp, []byte{StatusForbidden}) {
		t.Fatalf("media before sign-in: %x", resp)
	}
	c.call(loginRequest("alice", "correct horse", "x"))

	// Чтение кусками собирает файл побайтно, полный размер известен с первого ответа.
	var got []byte
	for offset := uint64(0); offset < uint64(len(track)); {
		_, resp, err := c.call(mediaRequest("stream", "id=1&maxBitRate=128&format=mp3", offset, 4<<20))
		if err != nil {
			t.Fatal(err)
		}
		_, code, total, body := parseMediaReply(t, resp)
		if code != http.StatusPartialContent || total != uint64(len(track)) || len(body) == 0 {
			t.Fatalf("chunk at %d: code %d total %d len %d", offset, code, total, len(body))
		}
		got = append(got, body...)
		offset += uint64(len(body))
	}
	if !bytes.Equal(got, track) {
		t.Fatal("reassembled track differs")
	}
	if lastQuery.Get("format") != "raw" || lastQuery.Get("maxBitRate") != "" || lastQuery.Get("u") != "alice" {
		t.Fatalf("stream must request original bytes as the session user: %v", lastQuery)
	}

	// Перемотка: чтение с середины.
	_, resp, _ := c.call(mediaRequest("download", "id=1", 5_000_000, 1000))
	if _, _, _, body := parseMediaReply(t, resp); !bytes.Equal(body, track[5_000_000:5_001_000]) {
		t.Fatal("seek read returned wrong bytes")
	}
	// За концом файла — 416 без тела.
	_, resp, _ = c.call(mediaRequest("stream", "id=1", uint64(len(track))+10, 1000))
	if _, code, _, body := parseMediaReply(t, resp); code != http.StatusRequestedRangeNotSatisfiable || len(body) > 200 {
		t.Fatalf("past end: %d %d", code, len(body))
	}
	_, resp, _ = c.call(mediaRequest("stream", "id=missing", 0, 1000))
	if _, code, _, _ := parseMediaReply(t, resp); code != http.StatusNotFound {
		t.Fatalf("missing track: %d", code)
	}

	// Источник без поддержки диапазонов: с нуля читается не больше запрошенного, с середины — отказ.
	ignoreRange = true
	_, resp, _ = c.call(mediaRequest("stream", "id=1", 0, 1000))
	if _, code, total, body := parseMediaReply(t, resp); code != http.StatusOK || total != uint64(len(track)) || !bytes.Equal(body, track[:1000]) {
		t.Fatalf("no-range from zero: %d %d %d", code, total, len(body))
	}
	if _, resp, _ := c.call(mediaRequest("stream", "id=1", 1000, 1000)); !bytes.Equal(resp, []byte{StatusRangeUnsupported}) {
		t.Fatalf("no-range from middle: %x", resp[:min(len(resp), 8)])
	}
	ignoreRange = false

	for name, body := range map[string][]byte{
		"zero length": mediaRequest("stream", "id=1", 0, 0),
		"huge length": mediaRequest("stream", "id=1", 0, MaxMediaChunk+1),
		"short":       {OpMedia, 1, 's'},
		"huge offset": mediaRequest("stream", "id=1", 1<<60, 10),
	} {
		if _, resp, _ := c.call(body); !bytes.Equal(resp, []byte{StatusBadRequest}) {
			t.Errorf("%s: %x", name, resp)
		}
	}
	if _, resp, _ := c.call(mediaRequest("getCoverArt", "id=1", 0, 10)); !bytes.Equal(resp, []byte{StatusForbidden}) {
		t.Fatalf("non-media method: %x", resp)
	}
}

// Настоящий httputil.ReverseProxy под HTTP-сервером: обрезка лишних байт не должна обрывать соединение канала.
func TestMediaThroughReverseProxyDoesNotAbort(t *testing.T) {
	track := bytes.Repeat([]byte("0123456789"), 400_000)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Header().Set("Content-Length", strconv.Itoa(len(track)))
		_, _ = w.Write(track) // диапазоны не поддерживаются
	}))
	defer origin.Close()
	target, _ := url.Parse(origin.URL)
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorLog = log.New(io.Discard, "", 0)

	f := newAccountsFixture(t)
	f.srv.handler = NewSubsonicHandler(proxy)
	c := f.client(t)
	c.call(loginRequest("alice", "correct horse", "x"))
	for range 3 {
		status, resp, err := c.call(mediaRequest("stream", "id=1", 0, 100_000))
		if status != http.StatusOK || err != nil {
			t.Fatalf("channel response aborted: %d %v", status, err)
		}
		if _, code, total, body := parseMediaReply(t, resp); code != http.StatusOK || total != uint64(len(track)) || !bytes.Equal(body, track[:100_000]) {
			t.Fatalf("capped chunk: %d %d %d", code, total, len(body))
		}
	}
	if _, resp, err := c.call(subsonicRequest("ping", "")); err != nil || len(resp) < 4 {
		t.Fatalf("channel unusable after capped reads: %v", err)
	}
}
