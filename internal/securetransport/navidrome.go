package securetransport

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"time"
)

// VerifyResult — итог проверки учётных данных в Navidrome.
type VerifyResult int

const (
	VerifyOK VerifyResult = iota
	// VerifyRejected — Navidrome ответил «неверное имя пользователя или пароль» (код 40).
	VerifyRejected
	// VerifyUnavailable — Navidrome не ответил или ответил иначе; устройства в этом случае не отзываются.
	VerifyUnavailable
)

// Verifier проверяет имя пользователя и пароль; Navidrome — единственный источник правды об аккаунтах.
type Verifier interface {
	Verify(ctx context.Context, username, password string) VerifyResult
}

// NavidromeVerifier проверяет учётные данные запросом /rest/ping с токен-авторизацией Subsonic.
type NavidromeVerifier struct {
	base   *url.URL
	client *http.Client
}

// NewNavidromeVerifier создаёт проверку для Navidrome по адресу base (внутренняя docker-сеть).
func NewNavidromeVerifier(base *url.URL, transport http.RoundTripper) *NavidromeVerifier {
	return &NavidromeVerifier{base: base, client: &http.Client{Transport: transport, Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (v *NavidromeVerifier) Verify(ctx context.Context, username, password string) VerifyResult {
	var saltBytes [8]byte
	if _, err := rand.Read(saltBytes[:]); err != nil {
		return VerifyUnavailable
	}
	salt := hex.EncodeToString(saltBytes[:])
	sum := md5.Sum([]byte(password + salt))
	u := v.base.JoinPath("rest", "ping")
	u.RawQuery = url.Values{
		"u": {username}, "t": {hex.EncodeToString(sum[:])}, "s": {salt},
		"v": {"1.16.1"}, "c": {"poisq-secure"}, "f": {"json"},
	}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return VerifyUnavailable
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return VerifyUnavailable // ошибку не логируем: в адресе запроса токен входа
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return VerifyUnavailable
	}
	var body struct {
		R struct {
			Status string `json:"status"`
			Error  struct {
				Code int `json:"code"`
			} `json:"error"`
		} `json:"subsonic-response"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body) != nil {
		return VerifyUnavailable
	}
	switch {
	case body.R.Status == "ok":
		return VerifyOK
	case body.R.Status == "failed" && body.R.Error.Code == 40:
		return VerifyRejected
	default:
		return VerifyUnavailable
	}
}
