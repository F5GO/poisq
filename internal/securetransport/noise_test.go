package securetransport

import (
	"bytes"
	"crypto/ecdh"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

type noiseVector struct {
	ProtocolName     string `json:"protocol_name"`
	InitPrologue     string `json:"init_prologue"`
	InitEphemeral    string `json:"init_ephemeral"`
	InitRemoteStatic string `json:"init_remote_static"`
	RespPrologue     string `json:"resp_prologue"`
	RespStatic       string `json:"resp_static"`
	RespEphemeral    string `json:"resp_ephemeral"`
	HandshakeHash    string `json:"handshake_hash"`
	Messages         []struct {
		Payload    string `json:"payload"`
		Ciphertext string `json:"ciphertext"`
	} `json:"messages"`
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func mustX25519(t *testing.T, s string) *ecdh.PrivateKey {
	t.Helper()
	k, err := ecdh.X25519().NewPrivateKey(mustHex(t, s))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// Транспортные сообщения вектора шифруются обычным CipherState Noise поверх ключей Split:
// так проверяется, что Split даёт правильные ключи направлений.
func TestNoiseNKOfficialVector(t *testing.T) {
	raw, err := os.ReadFile("testdata/noise-nk.json")
	if err != nil {
		t.Fatal(err)
	}
	var v noiseVector
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if v.ProtocolName != noiseProtocolName {
		t.Fatalf("vector protocol %q", v.ProtocolName)
	}
	respStatic := mustX25519(t, v.RespStatic)
	if !bytes.Equal(respStatic.PublicKey().Bytes(), mustHex(t, v.InitRemoteStatic)) {
		t.Fatal("vector remote static does not match responder static")
	}

	init, msg1, err := newNKInitiator(mustHex(t, v.InitPrologue), respStatic.PublicKey(), mustX25519(t, v.InitEphemeral), mustHex(t, v.Messages[0].Payload))
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(msg1) != v.Messages[0].Ciphertext {
		t.Fatalf("message 1 = %x", msg1)
	}
	resp, msg2, err := respondNK(mustHex(t, v.RespPrologue), respStatic, mustX25519(t, v.RespEphemeral), msg1, mustHex(t, v.Messages[1].Payload))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(resp.payload, mustHex(t, v.Messages[0].Payload)) || hex.EncodeToString(msg2) != v.Messages[1].Ciphertext {
		t.Fatalf("message 2 = %x", msg2)
	}
	got, err := init.readMessage2(msg2)
	if err != nil || !bytes.Equal(got.payload, mustHex(t, v.Messages[1].Payload)) {
		t.Fatalf("initiator read message 2: %v", err)
	}
	if hex.EncodeToString(got.handshakeHash[:]) != v.HandshakeHash || got.handshakeHash != resp.handshakeHash {
		t.Fatalf("handshake hash %x", got.handshakeHash)
	}
	if got.initiatorToResponder != resp.initiatorToResponder || got.responderToInitiator != resp.responderToInitiator {
		t.Fatal("split keys differ between sides")
	}

	var nonces [2]uint64
	for i, m := range v.Messages[2:] {
		dir := i % 2 // 0: инициатор → ответчик
		key := got.initiatorToResponder
		if dir == 1 {
			key = got.responderToInitiator
		}
		c, err := sealAESGCM(key[:], nonces[dir], nil, mustHex(t, m.Payload))
		if err != nil || hex.EncodeToString(c) != m.Ciphertext {
			t.Fatalf("transport message %d = %x, %v", i+2, c, err)
		}
		nonces[dir]++
	}
}

func TestNoiseNKRejectsTamperingAndWrongKey(t *testing.T) {
	static := mustX25519(t, "4a3acbfdb163dec651dfa3194dece676d437029c62a408b4c5ea9114246e4893")
	prologue := []byte("poisq-test")
	_, msg1, err := newNKInitiator(prologue, static.PublicKey(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := range msg1 {
		bad := append([]byte(nil), msg1...)
		bad[i] ^= 1
		if _, _, err := respondNK(prologue, static, nil, bad, nil); err == nil {
			t.Fatalf("accepted message 1 with byte %d flipped", i)
		}
	}
	if _, _, err := respondNK([]byte("other"), static, nil, msg1, nil); err == nil {
		t.Fatal("accepted a different prologue")
	}
	other := mustX25519(t, "bbdb4cdbd309f1a1f2e1456967fe288cadd6f712d65dc7b7793d5e63da6b375b")
	if _, _, err := respondNK(prologue, other, nil, msg1, nil); err == nil {
		t.Fatal("a different static key decrypted message 1")
	}
	if _, _, err := respondNK(prologue, static, nil, make([]byte, 48), nil); err == nil {
		t.Fatal("accepted a low-order ephemeral key")
	}
	if _, _, err := respondNK(prologue, static, nil, msg1[:40], nil); err == nil {
		t.Fatal("accepted a truncated message")
	}

	init, msg1, _ := newNKInitiator(prologue, static.PublicKey(), nil, nil)
	_, msg2, err := respondNK(prologue, static, nil, msg1, []byte("ok"))
	if err != nil {
		t.Fatal(err)
	}
	bad := append([]byte(nil), msg2...)
	bad[len(bad)-1] ^= 1
	if _, err := init.readMessage2(bad); err == nil {
		t.Fatal("accepted a tampered message 2")
	}
}
