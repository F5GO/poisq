package admin

// Данный файл api_public_access.go — политика публичного Subsonic-входа :4534 (Studio → Настройки → Navidrome).
// По умолчанию :4534 пропускает только Subsonic API, веб-интерфейс Navidrome закрыт.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/F5GO/poisq/internal/db"
	"github.com/F5GO/poisq/internal/security"
)

const publicAccessSettingKey = "public_access"

type publicAccessSettings struct {
	NavidromeWebOpen bool     `json:"navidromeWebOpen"`
	SecureOnly       bool     `json:"secureOnly"`
	SecureOnlyUsers  []string `json:"secureOnlyUsers"`
}

// publicAccessUpdate — частичное обновление: переданные поля меняются, остальные сохраняются.
type publicAccessUpdate struct {
	NavidromeWebOpen *bool     `json:"navidromeWebOpen"`
	SecureOnly       *bool     `json:"secureOnly"`
	SecureOnlyUsers  *[]string `json:"secureOnlyUsers"`
}

func currentPublicAccess(a *security.PublicAccess) publicAccessSettings {
	all, users := a.SecureOnly()
	if users == nil {
		users = []string{}
	}
	return publicAccessSettings{NavidromeWebOpen: a.NavidromeWebOpen(), SecureOnly: all, SecureOnlyUsers: users}
}

func applyPublicAccess(a *security.PublicAccess, st publicAccessSettings) {
	a.SetNavidromeWebOpen(st.NavidromeWebOpen)
	a.SetSecureOnly(st.SecureOnly, st.SecureOnlyUsers)
}

// cleanUsernames убирает пустые и повторы, приводит к нижнему регистру и ограничивает размер списка.
func cleanUsernames(in []string) ([]string, bool) {
	seen := map[string]bool{}
	out := []string{}
	for _, u := range in {
		u = strings.ToLower(strings.TrimSpace(u))
		if u == "" || seen[u] {
			continue
		}
		if len(u) > 128 || strings.ContainsAny(u, "\x00\r\n") {
			return nil, false
		}
		seen[u] = true
		out = append(out, u)
	}
	return out, len(out) <= 1000
}

// LoadPublicAccess применяет сохранённую политику. При ошибке чтения веб-интерфейс остаётся закрытым.
func LoadPublicAccess(poisqDB *sql.DB, access *security.PublicAccess) error {
	access.SetNavidromeWebOpen(false)
	if poisqDB == nil {
		return errors.New("база poisq не подключена")
	}
	raw, err := db.GetSetting(poisqDB, publicAccessSettingKey)
	if err != nil || raw == "" {
		return err
	}
	var st publicAccessSettings
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		return err
	}
	users, ok := cleanUsernames(st.SecureOnlyUsers)
	if !ok {
		return errors.New("некорректный список аккаунтов «только защищённый вход»")
	}
	st.SecureOnlyUsers = users
	applyPublicAccess(access, st)
	return nil
}

// SetPublicAccess подключает политику публичного входа, общую с прокси.
func (s *AdminServer) SetPublicAccess(a *security.PublicAccess) { s.publicAccess = a }

func (s *AdminServer) handlePublicAccess(w http.ResponseWriter, r *http.Request) {
	if s.publicAccess == nil || s.poisqDB == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "политика публичного входа не подключена")
		return
	}
	switch r.Method {
	case http.MethodGet:
	case http.MethodPost:
		var upd publicAccessUpdate
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&upd); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		st := currentPublicAccess(s.publicAccess)
		if upd.NavidromeWebOpen != nil {
			st.NavidromeWebOpen = *upd.NavidromeWebOpen
		}
		if upd.SecureOnly != nil {
			st.SecureOnly = *upd.SecureOnly
		}
		if upd.SecureOnlyUsers != nil {
			users, ok := cleanUsernames(*upd.SecureOnlyUsers)
			if !ok {
				writeJSONError(w, http.StatusBadRequest, "некорректный список аккаунтов")
				return
			}
			st.SecureOnlyUsers = users
		}
		// Без Secure Transport отмеченные пользователи остались бы без доступа извне.
		if enabled, _, _ := s.secureTransport.State(); (st.SecureOnly || len(st.SecureOnlyUsers) > 0) && !enabled {
			writeJSONError(w, http.StatusBadRequest, "сначала включите Secure Transport")
			return
		}
		raw, _ := json.Marshal(st)
		// Сначала сохраняем: политика в памяти не должна расходиться с той, что применится после перезапуска.
		if err := db.SetSetting(s.poisqDB, publicAccessSettingKey, string(raw)); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "не удалось сохранить настройку")
			return
		}
		applyPublicAccess(s.publicAccess, st)
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, currentPublicAccess(s.publicAccess))
}
