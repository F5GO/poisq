package securetransport

import (
	"bytes"
	"crypto/ecdh"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

// channelVector — общий вектор канала Go↔Swift (testdata/channel-v1.json): детерминированные эфемерные ключи
// и идентификатор сессии дают побайтно воспроизводимые сообщения. Ключи установки — из pairing-v1.json.
type channelVector struct {
	Comment         string       `json:"comment"`
	Epoch           uint32       `json:"epoch"`
	ClientEphemeral string       `json:"clientEphemeral"`
	ServerEphemeral string       `json:"serverEphemeral"`
	Hello           string       `json:"hello"`
	HelloResponse   string       `json:"helloResponse"`
	SessionID       string       `json:"sessionId"`
	SessionTTL      uint32       `json:"sessionTtl"`
	MaxRequest      uint32       `json:"maxRequest"`
	MaxRecord       uint32       `json:"maxRecord"`
	Calls           []vectorCall `json:"calls"`
}

type vectorCall struct {
	Counter  uint64 `json:"counter"`
	Request  string `json:"request"`
	Body     string `json:"body"`
	Response string `json:"response"`
	Records  string `json:"records"`
}

func vectorResponse(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i % 251)
	}
	return b
}

func buildChannelVector(t *testing.T) channelVector {
	t.Helper()
	cert, _ := vectorCertificate(t)
	working, _ := ecdh.X25519().NewPrivateKey(seq(0x41))
	clientEph, _ := ecdh.X25519().NewPrivateKey(seq(0x81))
	serverEph, _ := ecdh.X25519().NewPrivateKey(seq(0xa1))
	var sessionID [16]byte
	for i := range sessionID {
		sessionID[i] = 0xc0 + byte(i)
	}
	prologue := handshakePrologue(testInstallationID, cert.Epoch)
	init, msg1, err := newNKInitiator(prologue, working.PublicKey(), clientEph, nil)
	if err != nil {
		t.Fatal(err)
	}
	hello := append(binary.BigEndian.AppendUint32([]byte{wireVersion}, cert.Epoch), msg1...)
	payload := append([]byte(nil), sessionID[:]...)
	payload = binary.BigEndian.AppendUint32(payload, 120)
	payload = binary.BigEndian.AppendUint32(payload, MaxRequestSize)
	payload = binary.BigEndian.AppendUint32(payload, MaxRecordSize)
	server, msg2, err := respondNK(prologue, working, serverEph, msg1, payload)
	if err != nil {
		t.Fatal(err)
	}
	client, err := init.readMessage2(msg2)
	if err != nil || client.initiatorToResponder != server.initiatorToResponder {
		t.Fatal("handshake mismatch")
	}
	v := channelVector{
		Comment:         "Общий вектор канала v1. Ключи установки — из pairing-v1.json; эфемерные ключи клиента и сервера — seed 0x81..0xa0 и 0xa1..0xc0; session_id = c0..cf.",
		Epoch:           cert.Epoch,
		ClientEphemeral: hex.EncodeToString(seq(0x81)),
		ServerEphemeral: hex.EncodeToString(seq(0xa1)),
		Hello:           hex.EncodeToString(hello),
		HelloResponse:   hex.EncodeToString(msg2),
		SessionID:       hex.EncodeToString(sessionID[:]),
		SessionTTL:      120, MaxRequest: MaxRequestSize, MaxRecord: MaxRecordSize,
	}
	for _, c := range []struct {
		counter  uint64
		request  []byte
		response []byte
	}{
		{1, append([]byte{OpPing}, "poisq"...), append([]byte{StatusOK}, "poisq"...)},
		{2, []byte{OpPing}, vectorResponse(MaxRecordSize + 5)},
		{7, []byte{OpPing}, nil},
	} {
		header := callHeader(sessionID, c.counter)
		ct, _ := sealAESGCM(deriveKey(client.initiatorToResponder, requestLabel, c.counter), 0, recordAAD(header, 0, true), c.request)
		var records bytes.Buffer
		rw := newRecordWriter(&records, nil, deriveKey(server.responderToInitiator, responseLabel, c.counter), header)
		_, _ = rw.Write(c.response)
		_ = rw.Close()
		v.Calls = append(v.Calls, vectorCall{
			Counter: c.counter, Request: hex.EncodeToString(c.request), Body: hex.EncodeToString(append(header, ct...)),
			Response: hex.EncodeToString(c.response), Records: hex.EncodeToString(records.Bytes()),
		})
	}
	return v
}

func TestChannelSharedVector(t *testing.T) {
	want := buildChannelVector(t)
	raw, err := os.ReadFile("testdata/channel-v1.json")
	if os.Getenv("POISQ_WRITE_VECTORS") == "1" {
		out, _ := json.MarshalIndent(want, "", "  ")
		if err := os.WriteFile("testdata/channel-v1.json", append(out, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	var got channelVector
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(got)
	b, _ := json.Marshal(want)
	if !bytes.Equal(a, b) {
		t.Fatal("testdata/channel-v1.json does not match the Go implementation (regenerate with POISQ_WRITE_VECTORS=1)")
	}
}
