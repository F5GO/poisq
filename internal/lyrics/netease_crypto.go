// Package lyrics предоставляет систему поиска, ранжирования и сохранения текстов песен (Lyrics).
//
// Данный файл netease_crypto.go реализует шифрование запросов NetEase «weapi». Старые открытые эндпоинты
// (/api/search/get/web) теперь возвращают зашифрованный hex-ответ вместо JSON, поэтому поиск и лирика
// запрашиваются через /weapi/... в штатном формате веб-клиента music.163.com:
//
//	params     = base64(AES-128-CBC(base64(AES-128-CBC(json, presetKey, iv)), secretKey, iv))
//	encSecKey  = hex(RSA-NoPadding(reverse(secretKey)))
//
// где secretKey — случайные 16 символов [a-zA-Z0-9]. Константы публичны (из веб-клиента и открытых реализаций).
package lyrics

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/url"
)

const (
	neteaseIV        = "0102030405060708"
	neteasePresetKey = "0CoJUm6Qyw8W8jud"
	neteaseBase62    = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	neteasePublicKey = `-----BEGIN PUBLIC KEY-----
MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDgtQn2JZ34ZC28NWYpAUd98iZ37BUrX/aKzmFbt7clFSs6sXqHauqKWqdtLkF2KexO40H1YTX8z2lSgBBOAxLsvaklV8k4cBFK9snQXE9/DDaFt6Rr7iVZMldczhC0JNgTz+SHXT6CBHuX3e9SdB1Ua44oncaTWz7OBGLbCiK45wIDAQAB
-----END PUBLIC KEY-----`
)

func aesCBCBase64(plain []byte, key string) (string, error) {
	block, err := aes.NewCipher([]byte(key))
	if err != nil {
		return "", err
	}
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	padded := append(append([]byte(nil), plain...), bytes.Repeat([]byte{byte(pad)}, pad)...)
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, []byte(neteaseIV)).CryptBlocks(out, padded)
	return base64.StdEncoding.EncodeToString(out), nil
}

func neteaseRandomSecret() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	out := make([]byte, 16)
	for i, b := range buf {
		out[i] = neteaseBase62[int(b)%len(neteaseBase62)]
	}
	return string(out), nil
}

func neteaseRSANoPadding(msg []byte) (string, error) {
	blk, _ := pem.Decode([]byte(neteasePublicKey))
	if blk == nil {
		return "", fmt.Errorf("netease: не удалось разобрать публичный ключ")
	}
	pubAny, err := x509.ParsePKIXPublicKey(blk.Bytes)
	if err != nil {
		return "", err
	}
	pub, ok := pubAny.(*rsa.PublicKey)
	if !ok {
		return "", fmt.Errorf("netease: ключ не RSA")
	}
	m := new(big.Int).SetBytes(msg)
	c := new(big.Int).Exp(m, big.NewInt(int64(pub.E)), pub.N)
	out := make([]byte, (pub.N.BitLen()+7)/8)
	c.FillBytes(out)
	return hex.EncodeToString(out), nil
}

// neteaseWeapiForm шифрует тело запроса в форму {params, encSecKey}. secret можно передать для тестов ("" — случайный).
func neteaseWeapiForm(payload any, secret string) (url.Values, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if secret == "" {
		if secret, err = neteaseRandomSecret(); err != nil {
			return nil, err
		}
	}
	first, err := aesCBCBase64(raw, neteasePresetKey)
	if err != nil {
		return nil, err
	}
	params, err := aesCBCBase64([]byte(first), secret)
	if err != nil {
		return nil, err
	}
	rev := []byte(secret)
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	enc, err := neteaseRSANoPadding(rev)
	if err != nil {
		return nil, err
	}
	form := url.Values{}
	form.Set("params", params)
	form.Set("encSecKey", enc)
	return form, nil
}
