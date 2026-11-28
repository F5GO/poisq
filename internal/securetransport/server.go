package securetransport

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"io"
	"net/http"
	"sync"
	"time"
)

const (
	// PreAuthSessionTTL — срок сессии до входа: в ней разрешён только вход.
	PreAuthSessionTTL = 2 * time.Minute
	// AuthenticatedSessionTTL — срок сессии после входа.
	AuthenticatedSessionTTL = 24 * time.Hour
	// MaxSessions ограничивает таблицу сессий одной установки.
	MaxSessions = 1024
	// MaxSessionsPerDevice — при превышении вытесняется самая старая сессия этого же устройства.
	MaxSessionsPerDevice = 8

	helloRatePerMinute = 20
	helloBurst         = 20
	maxRateEntries     = 4096
)

// Session — подтверждённая handshake сессия канала.
type Session struct {
	id      [16]byte
	c2s     [32]byte
	s2c     [32]byte
	expires time.Time

	ip string

	mu       sync.Mutex
	window   replayWindow
	user     string // пусто до входа
	deviceID string
	password string // пароль Navidrome для запросов от имени пользователя; только в памяти
}

// User возвращает пользователя Navidrome сессии и пароль для запросов от его имени; ok — сессия после входа.
func (s *Session) User() (user, password string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.user, s.password, s.user != ""
}

func (s *Session) device() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deviceID
}

// Handler обрабатывает расшифрованный запрос. Ответ, записанный в w, канал шифрует и делит на записи.
// Ошибка после начала ответа обрывает поток без последней записи, и клиент отвергает ответ как усечённый.
type Handler func(ctx context.Context, s *Session, request []byte, w io.Writer) error

// Server обслуживает PathPrefix на публичном входе Studio.
type Server struct {
	inst     *Installation
	handler  Handler
	clientIP func(*http.Request) string
	now      func() time.Time

	accounts *Accounts
	hellos   *rateLimiter

	mu       sync.Mutex
	sessions map[[16]byte]*Session
}

// NewServer создаёт обработчик протокола. clientIP возвращает адрес клиента для ограничения частоты handshake;
// ему можно доверять только после очистки заголовков от недоверенных прокси.
func NewServer(inst *Installation, handler Handler, clientIP func(*http.Request) string) *Server {
	return &Server{
		inst: inst, handler: handler, clientIP: clientIP, now: time.Now,
		sessions: make(map[[16]byte]*Session), hellos: newRateLimiter(helloRatePerMinute, helloBurst, maxRateEntries),
	}
}

// SetAccounts подключает вход и устройства. Без них операции входа отвечают StatusUnavailable.
func (s *Server) SetAccounts(a *Accounts) {
	a.onRevoke = s.dropDeviceSessions
	s.accounts = a
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	enabled, _, keys := s.inst.State()
	if !enabled || keys == nil {
		s.dropSessions()
		http.NotFound(w, r)
		return
	}
	switch r.URL.Path {
	case PathPrefix + "certificate":
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		writeBinary(w, http.StatusOK, keys.CurrentCertificate())
	case PathPrefix + "hello":
		s.hello(w, r, keys)
	case PathPrefix + "call":
		s.call(w, r)
	default:
		http.NotFound(w, r)
	}
}

func writeBinary(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// readBody читает тело не длиннее limit; более длинное тело — ошибка.
func readBody(w http.ResponseWriter, r *http.Request, limit int) ([]byte, int) {
	if r.Method != http.MethodPost {
		return nil, http.StatusMethodNotAllowed
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, int64(limit)))
	if err != nil {
		return nil, http.StatusRequestEntityTooLarge
	}
	return body, 0
}

func (s *Server) hello(w http.ResponseWriter, r *http.Request, keys *KeyStore) {
	body, status := readBody(w, r, helloSize)
	if status != 0 {
		w.WriteHeader(status)
		return
	}
	now := s.now()
	ip := s.clientIP(r)
	if !s.hellos.allow(ip, now) {
		w.WriteHeader(http.StatusTooManyRequests)
		return
	}
	if len(body) != helloSize || body[0] != wireVersion {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	epoch := binary.BigEndian.Uint32(body[1:5])
	static, ok := keys.WorkingKey(epoch, now)
	if !ok {
		// Неизвестная или истёкшая эпоха: отдаём актуальный публичный сертификат, клиент проверит его по корню.
		writeBinary(w, http.StatusConflict, keys.CurrentCertificate())
		return
	}
	sess := &Session{expires: now.Add(PreAuthSessionTTL), ip: ip}
	if _, err := rand.Read(sess.id[:]); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	payload := make([]byte, 0, helloPayloadSize)
	payload = append(payload, sess.id[:]...)
	payload = binary.BigEndian.AppendUint32(payload, uint32(PreAuthSessionTTL/time.Second))
	payload = binary.BigEndian.AppendUint32(payload, MaxRequestSize)
	payload = binary.BigEndian.AppendUint32(payload, MaxRecordSize)
	result, message2, err := respondNK(handshakePrologue(keys.InstallationID(), epoch), static, nil, body[5:], payload)
	// Первое сообщение повторяемо и не обладает прямой секретностью: сервер не принимает в нём никаких данных.
	if err != nil || len(result.payload) != 0 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	sess.c2s, sess.s2c = result.initiatorToResponder, result.responderToInitiator
	if !s.addSession(sess, now) {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	writeBinary(w, http.StatusOK, message2)
}

func (s *Server) call(w http.ResponseWriter, r *http.Request) {
	body, status := readBody(w, r, callHeaderSize+MaxRequestSize+noiseTagSize)
	if status != 0 {
		w.WriteHeader(status)
		return
	}
	if len(body) < callHeaderSize+noiseTagSize || body[0] != wireVersion {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	header := body[:callHeaderSize]
	var id [16]byte
	copy(id[:], header[1:17])
	counter := binary.BigEndian.Uint64(header[17:25])
	sess := s.session(id, s.now())
	if sess == nil {
		w.WriteHeader(http.StatusGone) // клиент выполняет новый handshake
		return
	}
	request, err := openAESGCM(deriveKey(sess.c2s, requestLabel, counter), 0, recordAAD(header, 0, true), body[callHeaderSize:])
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	// Окно отмечается только после проверки подлинности: чужой мусор не может «сжечь» счётчики клиента.
	sess.mu.Lock()
	fresh := sess.window.accept(counter)
	sess.mu.Unlock()
	if !fresh {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	flush := func() {}
	if f, ok := w.(http.Flusher); ok {
		flush = f.Flush
	}
	rw := newRecordWriter(w, flush, deriveKey(sess.s2c, responseLabel, counter), header)
	if err := s.dispatch(r.Context(), sess, request, rw); err != nil {
		return
	}
	_ = rw.Close()
}

func (s *Server) addSession(sess *Session, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sessions) >= MaxSessions {
		for id, existing := range s.sessions {
			if !now.Before(existing.expires) {
				delete(s.sessions, id)
			}
		}
		// Действующие сессии не вытесняем: иначе поток handshake выбивал бы настоящих клиентов.
		if len(s.sessions) >= MaxSessions {
			return false
		}
	}
	s.sessions[sess.id] = sess
	return true
}

func (s *Server) session(id [16]byte, now time.Time) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.sessions[id]
	if sess != nil && !now.Before(sess.expires) {
		delete(s.sessions, id)
		return nil
	}
	return sess
}

func (s *Server) dropSessions() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sessions) > 0 {
		s.sessions = make(map[[16]byte]*Session)
	}
}

// dispatch выполняет операции канала. До входа доступны только проверочный обмен, вход и продолжение по токену.
func (s *Server) dispatch(ctx context.Context, sess *Session, request []byte, w io.Writer) error {
	if len(request) == 0 {
		_, err := w.Write([]byte{StatusBadRequest})
		return err
	}
	switch request[0] {
	case OpPing:
		return PingHandler(ctx, sess, request, w)
	case OpLogin, OpResume, OpSignOut:
		if s.accounts == nil {
			_, err := w.Write([]byte{StatusUnavailable})
			return err
		}
		return s.accounts.handle(ctx, s, sess, request, w)
	}
	if _, _, ok := sess.User(); !ok {
		_, err := w.Write([]byte{StatusForbidden})
		return err
	}
	return s.handler(ctx, sess, request, w)
}

// authenticate привязывает сессию к устройству и продлевает её; лишние сессии этого устройства вытесняются.
func (s *Server) authenticate(sess *Session, d Device, password string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess.mu.Lock()
	sess.user, sess.deviceID, sess.password = d.Username, d.ID, password
	sess.mu.Unlock()
	sess.expires = now.Add(AuthenticatedSessionTTL)
	var same []*Session
	for _, other := range s.sessions {
		if other != sess && other.device() == d.ID {
			same = append(same, other)
		}
	}
	for len(same) >= MaxSessionsPerDevice {
		oldest := 0
		for i := range same {
			if same[i].expires.Before(same[oldest].expires) {
				oldest = i
			}
		}
		delete(s.sessions, same[oldest].id)
		same = append(same[:oldest], same[oldest+1:]...)
	}
}

// dropDeviceSessions завершает сессии отозванных устройств.
func (s *Server) dropDeviceSessions(ids ...string) {
	if len(ids) == 0 {
		return
	}
	revoked := make(map[string]bool, len(ids))
	for _, id := range ids {
		revoked[id] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, sess := range s.sessions {
		if revoked[sess.device()] {
			delete(s.sessions, key)
		}
	}
}
