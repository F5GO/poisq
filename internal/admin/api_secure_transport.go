package admin

// Данный файл api_secure_transport.go — настройки Secure Transport (Studio → Настройки → Secure Transport):
// включение, внешний HTTPS-адрес для QR, отпечаток корневого ключа и сам QR сопряжения.
// Панель доступна только из домашней сети, поэтому QR выдаётся по доверенному маршруту.

import (
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/F5GO/poisq/internal/db"
	"github.com/F5GO/poisq/internal/securetransport"
)

const secureTransportSettingKey = "secure_transport"

type secureTransportSettings struct {
	Enabled      bool   `json:"enabled"`
	PublicOrigin string `json:"publicOrigin"`
}

type secureTransportStatus struct {
	secureTransportSettings
	KeysCreated          bool   `json:"keysCreated"`
	InstallationID       string `json:"installationId,omitempty"`
	Fingerprint          string `json:"fingerprint,omitempty"`
	PairingCode          string `json:"pairingCode,omitempty"`
	CurrentEpoch         uint32 `json:"currentEpoch,omitempty"`
	CertificateExpiresAt int64  `json:"certificateExpiresAt,omitempty"`
}

// LoadSecureTransport применяет сохранённые настройки. При ошибке Secure Transport остаётся выключенным.
func LoadSecureTransport(poisqDB *sql.DB, inst *securetransport.Installation, now time.Time) error {
	if poisqDB == nil {
		return errors.New("база poisq не подключена")
	}
	raw, err := db.GetSetting(poisqDB, secureTransportSettingKey)
	if err != nil || raw == "" {
		return err
	}
	var st secureTransportSettings
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		return err
	}
	if err := inst.Configure(st.Enabled, st.PublicOrigin, now); err != nil {
		_ = inst.Configure(false, "", now)
		return err
	}
	return nil
}

// SetSecureTransport подключает состояние Secure Transport, общее с прокси.
func (s *AdminServer) SetSecureTransport(inst *securetransport.Installation) {
	s.secureTransport = inst
}

func (s *AdminServer) handleSecureTransport(w http.ResponseWriter, r *http.Request) {
	if s.secureTransport == nil || s.poisqDB == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "Secure Transport не подключён")
		return
	}
	switch r.Method {
	case http.MethodGet:
	case http.MethodPost:
		var st secureTransportSettings
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048)).Decode(&st); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		prevEnabled, prevOrigin, _ := s.secureTransport.State()
		now := time.Now()
		if err := s.secureTransport.Configure(st.Enabled, st.PublicOrigin, now); err != nil {
			writeJSONError(w, http.StatusBadRequest, "нужен внешний адрес вида https://example.com без пути и параметров")
			return
		}
		enabled, origin, _ := s.secureTransport.State()
		raw, _ := json.Marshal(secureTransportSettings{Enabled: enabled, PublicOrigin: origin})
		if err := db.SetSetting(s.poisqDB, secureTransportSettingKey, string(raw)); err != nil {
			_ = s.secureTransport.Configure(prevEnabled, prevOrigin, now)
			writeJSONError(w, http.StatusInternalServerError, "не удалось сохранить настройку")
			return
		}
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, s.secureTransportStatus())
}

func (s *AdminServer) secureTransportStatus() secureTransportStatus {
	enabled, origin, keys := s.secureTransport.State()
	st := secureTransportStatus{secureTransportSettings: secureTransportSettings{Enabled: enabled, PublicOrigin: origin}}
	if keys == nil {
		return st
	}
	id := keys.InstallationID()
	st.KeysCreated = true
	st.InstallationID = hex.EncodeToString(id[:])
	st.Fingerprint = securetransport.KeyFingerprint(keys.RootPublicKey())
	if cert, err := securetransport.ParseWorkingCertificate(keys.CurrentCertificate(), keys.RootPublicKey(), id); err == nil {
		st.CurrentEpoch = cert.Epoch
		st.CertificateExpiresAt = cert.NotAfter.Unix()
	}
	if enabled {
		st.PairingCode, _ = keys.PairingCode(origin)
	}
	return st
}

// handleSecureTransportQR отдаёт QR сопряжения как SVG, только когда Secure Transport включён.
func (s *AdminServer) handleSecureTransportQR(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if s.secureTransport == nil {
		http.NotFound(w, r)
		return
	}
	enabled, origin, keys := s.secureTransport.State()
	if !enabled || keys == nil {
		http.NotFound(w, r)
		return
	}
	code, err := keys.PairingCode(origin)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	svg, err := qrSVG(code)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "не удалось построить QR")
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	_, _ = w.Write([]byte(svg))
}

// SetSecureAccounts подключает устройства Secure Transport для списка и отзыва в панели.
func (s *AdminServer) SetSecureAccounts(a *securetransport.Accounts) { s.secureAccounts = a }

type secureDeviceRevokeRequest struct {
	ID string `json:"id"`
}

// handleSecureDevices: GET — устройства, POST {id} — отзыв устройства с завершением его сессий.
func (s *AdminServer) handleSecureDevices(w http.ResponseWriter, r *http.Request) {
	if s.secureAccounts == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "устройства Secure Transport не подключены")
		return
	}
	switch r.Method {
	case http.MethodGet:
	case http.MethodPost:
		var req secureDeviceRevokeRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil || len(req.ID) != 32 {
			writeJSONError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		ok, err := s.secureAccounts.Revoke(r.Context(), req.ID)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "не удалось отозвать устройство")
			return
		}
		if !ok {
			writeJSONError(w, http.StatusNotFound, "устройство не найдено")
			return
		}
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	devices, err := s.secureAccounts.Devices(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "не удалось прочитать устройства")
		return
	}
	if devices == nil {
		devices = []securetransport.Device{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": devices})
}
