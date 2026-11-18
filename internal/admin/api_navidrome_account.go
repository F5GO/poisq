package admin

// Данный файл api_navidrome_account.go — служебная учётная запись Navidrome для Studio (Настройки → Navidrome).
// Она нужна только запасным путям: поток трека, когда файла нет в /music, и /rest/getCoverArt, когда картинки нет
// на диске и в кэше Navidrome. Имя хранится меткой ключа, пароль — зашифрованным ключом в пуле (провайдер navidrome).

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const navidromeAccountProvider = "navidrome"

var errNoNavidromeAccount = errors.New("служебная учётная запись Navidrome не задана")

type navidromeAccountRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Clear    bool   `json:"clear,omitempty"`
}

func (s *AdminServer) handleNavidromeAccount(w http.ResponseWriter, r *http.Request) {
	if s.keyPool == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "пул ключей не подключён")
		return
	}
	switch r.Method {
	case http.MethodGet:
	case http.MethodPost:
		var req navidromeAccountRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		user := strings.TrimSpace(req.Username)
		if !req.Clear {
			if user == "" || req.Password == "" {
				writeJSONError(w, http.StatusBadRequest, "укажите имя пользователя и пароль Navidrome")
				return
			}
			if err := s.pingNavidrome(r.Context(), user, req.Password); err != nil {
				writeJSONError(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		s.deleteNavidromeAccount()
		if !req.Clear {
			if _, err := s.keyPool.AddKey(navidromeAccountProvider, user, req.Password); err != nil {
				writeJSONError(w, http.StatusBadRequest, err.Error())
				return
			}
		}
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	username := ""
	if keys, err := s.keyPool.ListKeys(); err == nil {
		for _, k := range keys {
			if k.Provider == navidromeAccountProvider {
				username = k.Label
				break
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"configured": username != "", "username": username})
}

func (s *AdminServer) deleteNavidromeAccount() {
	keys, err := s.keyPool.ListKeys()
	if err != nil {
		return
	}
	for _, k := range keys {
		if k.Provider == navidromeAccountProvider {
			_ = s.keyPool.DeleteKey(k.ID)
		}
	}
}

// navidromeURLFor собирает адрес Subsonic-метода с токен-входом (t = md5(пароль + соль), пароль не уходит в адрес).
func (s *AdminServer) navidromeURLFor(method string, params url.Values, user, password string) string {
	salt := make([]byte, 8)
	_, _ = rand.Read(salt)
	saltHex := hex.EncodeToString(salt)
	token := md5.Sum([]byte(password + saltHex)) // md5 требует протокол Subsonic
	q := url.Values{}
	for k, v := range params {
		q[k] = v
	}
	q.Set("u", user)
	q.Set("t", hex.EncodeToString(token[:]))
	q.Set("s", saltHex)
	q.Set("v", "1.16.1")
	q.Set("c", "poisq-studio")
	return s.navidromeURL + "/rest/" + method + "?" + q.Encode()
}

// navidromeRequest готовит GET к Navidrome от служебной учётной записи Studio.
func (s *AdminServer) navidromeRequest(ctx context.Context, method string, params url.Values) (*http.Request, error) {
	if s.keyPool == nil || s.navidromeURL == "" {
		return nil, errNoNavidromeAccount
	}
	k, err := s.keyPool.Acquire(navidromeAccountProvider)
	if err != nil {
		return nil, errNoNavidromeAccount
	}
	return http.NewRequestWithContext(ctx, http.MethodGet, s.navidromeURLFor(method, params, k.Label, k.PlainKey), nil)
}

func (s *AdminServer) pingNavidrome(ctx context.Context, user, password string) error {
	if s.navidromeURL == "" {
		return errors.New("адрес Navidrome не задан (NAVIDROME_URL)")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.navidromeURLFor("ping", url.Values{"f": {"json"}}, user, password), nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		var ue *url.Error // в адресе запроса токен входа — наружу отдаём только причину
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return errors.New("Navidrome недоступен: " + err.Error())
	}
	defer resp.Body.Close()
	var body struct {
		R struct {
			Status string `json:"status"`
		} `json:"subsonic-response"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body) != nil || body.R.Status != "ok" {
		return errors.New("Navidrome не принял имя пользователя или пароль")
	}
	return nil
}
