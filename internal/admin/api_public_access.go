package admin

// Данный файл api_public_access.go — политика публичного Subsonic-входа :4534 (Studio → Настройки → Navidrome).
// По умолчанию :4534 пропускает только Subsonic API, веб-интерфейс Navidrome закрыт.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/F5GO/poisq/internal/db"
	"github.com/F5GO/poisq/internal/security"
)

const publicAccessSettingKey = "public_access"

type publicAccessSettings struct {
	NavidromeWebOpen bool `json:"navidromeWebOpen"`
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
	access.SetNavidromeWebOpen(st.NavidromeWebOpen)
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
		var st publicAccessSettings
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&st); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		raw, _ := json.Marshal(st)
		// Сначала сохраняем: политика в памяти не должна расходиться с той, что применится после перезапуска.
		if err := db.SetSetting(s.poisqDB, publicAccessSettingKey, string(raw)); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "не удалось сохранить настройку")
			return
		}
		s.publicAccess.SetNavidromeWebOpen(st.NavidromeWebOpen)
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, publicAccessSettings{NavidromeWebOpen: s.publicAccess.NavidromeWebOpen()})
}
