package securetransport

import (
	"bytes"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
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
