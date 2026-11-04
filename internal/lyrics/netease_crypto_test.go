package lyrics

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"testing"
)

func aesCBCDecryptBase64(t *testing.T, b64, key string) []byte {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := aes.NewCipher([]byte(key))
	out := make([]byte, len(raw))
	cipher.NewCBCDecrypter(block, []byte(neteaseIV)).CryptBlocks(out, raw)
	return out[:len(out)-int(out[len(out)-1])]
}

func TestNeteaseWeapiFormRoundTrip(t *testing.T) {
	const secret = "abcdEFGH12345678"
	form, err := neteaseWeapiForm(map[string]any{"s": "Green Day", "type": 1}, secret)
	if err != nil {
		t.Fatal(err)
	}
	// Снимаем два слоя AES: сначала ключом secret, затем presetKey
	inner := aesCBCDecryptBase64(t, form.Get("params"), secret)
	plain := aesCBCDecryptBase64(t, string(inner), neteasePresetKey)

	var got map[string]any
	if err := json.Unmarshal(plain, &got); err != nil {
		t.Fatalf("не JSON после расшифровки: %v (%q)", err, plain)
	}
	if got["s"] != "Green Day" {
		t.Fatalf("неверное тело: %v", got)
	}
	if len(form.Get("encSecKey")) != 256 {
		t.Fatalf("encSecKey должен быть 256 hex-символов, а не %d", len(form.Get("encSecKey")))
	}
}
