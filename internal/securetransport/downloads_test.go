package securetransport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"
)

func pattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*13 + i>>8)
	}
	return b
}

// openBlob — эталонная расшифровка (как в Swift): порядок, признак последнего куска и число кусков.
func openBlob(t *testing.T, key []byte, size int64, cipher []byte) []byte {
	t.Helper()
	total := uint64(blobChunks(size))
	if int64(len(cipher)) != BlobCipherSize(size) {
		t.Fatalf("cipher size %d, want %d", len(cipher), BlobCipherSize(size))
	}
	var out []byte
	for i := uint64(0); i < total; i++ {
		end := min(int64(i+1)*blobCipherChunk, int64(len(cipher)))
		p, err := openAESGCM(key, i, blobAAD(i, total, i == total-1), cipher[int64(i)*blobCipherChunk:end])
		if err != nil {
			t.Fatalf("chunk %d: %v", i, err)
		}
		out = append(out, p...)
	}
	return out
}

func TestBlobReaderRangesAndSizes(t *testing.T) {
	key := bytes.Repeat([]byte{9}, 32)
	for _, size := range []int{0, 1, BlobChunk - 1, BlobChunk, BlobChunk + 1, 3*BlobChunk*blobWindow/2 + 77} {
		plain := pattern(size)
		var calls int
		r := newBlobReader(key, int64(size), func(off, n int64) ([]byte, error) { calls++; return plain[off : off+n], nil })
		full, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		if got := openBlob(t, key, int64(size), full); !bytes.Equal(got, plain) {
			t.Fatalf("size %d: roundtrip differs", size)
		}
		// Любой диапазон совпадает с тем же местом полного шифротекста: докачка с произвольного байта.
		for _, start := range []int64{0, 1, blobCipherChunk - 1, blobCipherChunk, int64(len(full)) / 2, int64(len(full)) - 1} {
			if start >= int64(len(full)) {
				continue
			}
			if _, err := r.Seek(start, io.SeekStart); err != nil {
				t.Fatal(err)
			}
			part, err := io.ReadAll(r)
			if err != nil || !bytes.Equal(part, full[start:]) {
				t.Fatalf("size %d range from %d differs: %v", size, start, err)
			}
		}
		_ = calls
	}
}

func TestTicketSealAndOpen(t *testing.T) {
	var key [32]byte
	key[0] = 1
	now := time.Unix(1_800_000_000, 0)
	in := Ticket{DeviceID: "00112233445566778899aabbccddeeff", SongID: "song-1", Size: 12345, Expires: now.Add(time.Hour)}
	in.Version[0], in.Key[0] = 7, 8
	token, err := sealTicket(key, in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := openTicket(key, token, now)
	if err != nil || out.DeviceID != in.DeviceID || out.SongID != in.SongID || out.Size != in.Size || out.Version != in.Version || out.Key != in.Key || !out.Expires.Equal(in.Expires) {
		t.Fatalf("ticket roundtrip: %+v %v", out, err)
	}
	if _, err := openTicket(key, token, now.Add(time.Hour)); err == nil {
		t.Fatal("expired ticket accepted")
	}
	other := key
	other[0] = 2
	if _, err := openTicket(other, token, now); err == nil {
		t.Fatal("ticket of another installation accepted")
	}
	raw, _ := base64.RawURLEncoding.DecodeString(token)
	for i := range raw {
		bad := append([]byte(nil), raw...)
		bad[i] ^= 1
		if _, err := openTicket(key, base64.RawURLEncoding.EncodeToString(bad), now); err == nil {
			t.Fatalf("tampered byte %d accepted", i)
		}
	}
	for _, bad := range []Ticket{{DeviceID: "zz", SongID: "a"}, {DeviceID: in.DeviceID}, {DeviceID: in.DeviceID, SongID: string(make([]byte, 300))}} {
		if _, err := sealTicket(key, bad); err == nil {
			t.Errorf("sealed malformed ticket %+v", bad.SongID)
		}
	}
}

// fakeLibrary — Navidrome с Range, Last-Modified и проверкой пользователя.
type fakeLibrary struct {
	mu           sync.Mutex
	file         []byte
	lastModified string
	requests     int
}

func (f *fakeLibrary) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
	q := r.URL.Query()
	if r.URL.Path != "/rest/stream" || q.Get("id") != "t1" || q.Get("format") != "raw" || q.Get("u") != "alice" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if f.lastModified != "" {
		w.Header().Set("Last-Modified", f.lastModified)
	}
	w.Header().Set("Content-Type", "audio/flac")
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(f.file))
}

func ticketRequest(id string) []byte { return append([]byte{OpTicket, byte(len(id))}, id...) }

type issued struct {
	token string
	key   []byte
	size  int64
}

func parseTicket(t *testing.T, resp []byte) issued {
	t.Helper()
	if len(resp) < 3 || resp[0] != StatusOK {
		t.Fatalf("ticket reply %x", resp)
	}
	n := int(binary.BigEndian.Uint16(resp[1:3]))
	rest := resp[3+n:]
	if len(rest) != 32+8+4 || binary.BigEndian.Uint32(rest[40:]) != BlobChunk {
		t.Fatalf("ticket tail %x", rest)
	}
	return issued{token: string(resp[3 : 3+n]), key: rest[:32], size: int64(binary.BigEndian.Uint64(rest[32:40]))}
}

func getBlob(t *testing.T, base, token, rangeHeader string) (int, []byte, http.Header) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, base+PathPrefix+"blob/"+token, nil)
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body, resp.Header
}

func TestOfflineDownloadEndToEnd(t *testing.T) {
	lib := &fakeLibrary{file: pattern(2*BlobChunk*blobWindow + 5000), lastModified: "Wed, 01 Jan 2026 00:00:00 GMT"}
	f := newAccountsFixture(t)
	f.srv.SetUpstream(lib)
	c := f.client(t)
	if _, resp, _ := c.call(ticketRequest("t1")); !bytes.Equal(resp, []byte{StatusForbidden}) {
		t.Fatalf("ticket before sign-in: %x", resp)
	}
	_, resp, _ := c.call(loginRequest("alice", "correct horse", "x"))
	_, deviceID := credential(t, resp)

	if _, resp, _ := c.call(ticketRequest("other")); !bytes.Equal(resp, []byte{StatusRangeUnsupported}) {
		t.Fatalf("ticket for a file the user can't read: %x", resp)
	}
	_, resp, _ = c.call(ticketRequest("t1"))
	tk := parseTicket(t, resp)
	if tk.size != int64(len(lib.file)) {
		t.Fatalf("ticket size %d", tk.size)
	}

	status, cipher, h := getBlob(t, f.ts.URL, tk.token, "")
	if status != http.StatusOK || h.Get("Cache-Control") != "no-store" || h.Get("Content-Type") != "application/octet-stream" || h.Get("ETag") == "" {
		t.Fatalf("blob: %d %v", status, h)
	}
	if !bytes.Equal(openBlob(t, tk.key, tk.size, cipher), lib.file) {
		t.Fatal("decrypted download differs")
	}
	if bytes.Contains(cipher, lib.file[:4096]) {
		t.Fatal("plaintext visible in the blob")
	}
	// Докачка: Range с произвольного байта даёт те же байты, что и полный ответ.
	from := len(cipher)/2 + 3
	status, part, _ := getBlob(t, f.ts.URL, tk.token, "bytes="+strconv.Itoa(from)+"-")
	if status != http.StatusPartialContent || !bytes.Equal(part, cipher[from:]) {
		t.Fatalf("resume: %d %d", status, len(part))
	}

	// Изменённый файл: отдача прекращается, новое содержимое тем же ключом не шифруется.
	lib.mu.Lock()
	lib.lastModified = "Thu, 02 Jan 2026 00:00:00 GMT"
	lib.mu.Unlock()
	if status, _, _ := getBlob(t, f.ts.URL, tk.token, ""); status != http.StatusConflict {
		t.Fatalf("changed file: %d", status)
	}
	lib.mu.Lock()
	lib.lastModified = "Wed, 01 Jan 2026 00:00:00 GMT"
	lib.mu.Unlock()

	for name, token := range map[string]string{"garbage": "AAAA", "empty-ish": "x", "slash": "a/b"} {
		if status, _, _ := getBlob(t, f.ts.URL, token, ""); status != http.StatusNotFound {
			t.Errorf("%s token: %d", name, status)
		}
	}
	f.srv.now = func() time.Time { return time.Now().Add(TicketLifetime + time.Minute) }
	if status, _, _ := getBlob(t, f.ts.URL, tk.token, ""); status != http.StatusNotFound {
		t.Fatalf("expired ticket: %d", status)
	}
	f.srv.now = time.Now
	if ok, _ := f.accounts.Revoke(context.Background(), deviceID); !ok {
		t.Fatal("revoke failed")
	}
	if status, _, _ := getBlob(t, f.ts.URL, tk.token, ""); status != http.StatusNotFound {
		t.Fatalf("ticket of a revoked device: %d", status)
	}
}

func TestTicketRefusedWithoutStableVersion(t *testing.T) {
	lib := &fakeLibrary{file: pattern(1000)} // нет Last-Modified и ETag
	f := newAccountsFixture(t)
	f.srv.SetUpstream(lib)
	c := f.client(t)
	c.call(loginRequest("alice", "correct horse", "x"))
	if _, resp, _ := c.call(ticketRequest("t1")); !bytes.Equal(resp, []byte{StatusRangeUnsupported}) {
		t.Fatalf("ticket without a file version: %x", resp)
	}
}

type blobVector struct {
	Comment  string `json:"comment"`
	Key      string `json:"key"`
	Size     int64  `json:"size"`
	PlainSHA string `json:"plainSha256"`
	Cipher   string `json:"cipherBase64"`
}

// Общий вектор: Swift расшифровывает этот шифротекст тем же алгоритмом.
func TestBlobSharedVector(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(0xb0 + i)
	}
	plain := pattern(BlobChunk + 4464)
	cipher, _ := io.ReadAll(newBlobReader(key, int64(len(plain)), func(off, n int64) ([]byte, error) { return plain[off : off+n], nil }))
	sum := sha256.Sum256(plain)
	want := blobVector{
		Comment: "Общий вектор офлайн-загрузки v1: открытый файл — pattern(n)[i] = byte(i*13 + i>>8), ключ b0..cf.",
		Key:     hex.EncodeToString(key), Size: int64(len(plain)), PlainSHA: hex.EncodeToString(sum[:]),
		Cipher: base64.StdEncoding.EncodeToString(cipher),
	}
	if os.Getenv("POISQ_WRITE_VECTORS") == "1" {
		out, _ := json.MarshalIndent(want, "", "  ")
		if err := os.WriteFile("testdata/blob-v1.json", append(out, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	raw, err := os.ReadFile("testdata/blob-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var got blobVector
	if err := json.Unmarshal(raw, &got); err != nil || got != want {
		t.Fatal("testdata/blob-v1.json does not match the Go implementation (regenerate with POISQ_WRITE_VECTORS=1)")
	}
}
