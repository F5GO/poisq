package securetransport

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxUsername = 128
	maxPassword = 1024

	// Попытки входа: по аккаунту и по адресу, с запасом на опечатки, но без перебора паролей.
	loginPerMinuteUser = 10.0 / 15
	loginBurstUser     = 10
	loginPerMinuteIP   = 30.0 / 15
	loginBurstIP       = 30
)

// Accounts — вход по каналу и токены устройств. Navidrome проверяет пароль; Studio хранит только устройства.
type Accounts struct {
	devices  *DeviceStore
	verifier Verifier
	now      func() time.Time
	users    *rateLimiter
	ips      *rateLimiter
	onRevoke func(ids ...string)
}

// NewAccounts подключает хранилище устройств и проверку учётных данных.
func NewAccounts(devices *DeviceStore, verifier Verifier) *Accounts {
	return &Accounts{
		devices: devices, verifier: verifier, now: time.Now,
		users: newRateLimiter(loginPerMinuteUser, loginBurstUser, maxRateEntries),
		ips:   newRateLimiter(loginPerMinuteIP, loginBurstIP, maxRateEntries),
	}
}

// Devices возвращает устройства для панели Studio.
func (a *Accounts) Devices(ctx context.Context) ([]Device, error) { return a.devices.List(ctx) }

// Revoke отзывает устройство из панели Studio и завершает его сессии.
func (a *Accounts) Revoke(ctx context.Context, id string) (bool, error) {
	ok, err := a.devices.Revoke(ctx, id)
	if ok && a.onRevoke != nil {
		a.onRevoke(id)
	}
	return ok, err
}

func (a *Accounts) revoked(ids ...string) {
	if a.onRevoke != nil {
		a.onRevoke(ids...)
	}
}

func writeStatus(w io.Writer, status byte) error {
	_, err := w.Write([]byte{status})
	return err
}

// credentialResponse: status · токен [32] · ID устройства [16] · срок сессии u32 (секунды).
func credentialResponse(w io.Writer, d Device, token [32]byte) error {
	id, err := hex.DecodeString(d.ID)
	if err != nil || len(id) != 16 {
		return errors.New("secure transport: malformed device ID")
	}
	out := append([]byte{StatusOK}, token[:]...)
	out = append(out, id...)
	out = binary.BigEndian.AppendUint32(out, uint32(AuthenticatedSessionTTL/time.Second))
	_, err = w.Write(out)
	return err
}

func (a *Accounts) handle(ctx context.Context, srv *Server, sess *Session, request []byte, w io.Writer) error {
	_, _, authenticated := sess.User()
	switch request[0] {
	case OpLogin:
		if authenticated {
			return writeStatus(w, StatusForbidden) // другой аккаунт — только в новой сессии
		}
		return a.login(ctx, srv, sess, request[1:], w)
	case OpResume:
		if authenticated {
			return writeStatus(w, StatusForbidden)
		}
		return a.resume(ctx, srv, sess, request[1:], w)
	default: // OpSignOut
		if !authenticated {
			return writeStatus(w, StatusForbidden)
		}
		id := sess.device()
		if _, err := a.devices.Revoke(ctx, id); err != nil {
			return writeStatus(w, StatusUnavailable)
		}
		a.revoked(id)
		return writeStatus(w, StatusOK)
	}
}

// parseLogin разбирает u8 len · имя · u16 len · пароль · u8 len · имя устройства; лишние байты — ошибка.
func parseLogin(b []byte) (user, password, device string, ok bool) {
	field := func(lenSize, limit int) (string, bool) {
		if len(b) < lenSize {
			return "", false
		}
		n := int(b[0])
		if lenSize == 2 {
			n = int(binary.BigEndian.Uint16(b))
		}
		b = b[lenSize:]
		if n > limit || len(b) < n || !utf8.Valid(b[:n]) {
			return "", false
		}
		v := string(b[:n])
		b = b[n:]
		return v, true
	}
	var okU, okP, okD bool
	user, okU = field(1, maxUsername)
	password, okP = field(2, maxPassword)
	device, okD = field(1, 255)
	return user, password, device, okU && okP && okD && len(b) == 0 && strings.TrimSpace(user) != "" && password != ""
}

func (a *Accounts) login(ctx context.Context, srv *Server, sess *Session, body []byte, w io.Writer) error {
	user, password, deviceName, ok := parseLogin(body)
	if !ok {
		return writeStatus(w, StatusBadRequest)
	}
	now := a.now()
	if !a.ips.allow(sess.ip, now) || !a.users.allow(strings.ToLower(user), now) {
		return writeStatus(w, StatusRateLimited)
	}
	switch a.verifier.Verify(ctx, user, password) {
	case VerifyRejected:
		return writeStatus(w, StatusUnauthorized)
	case VerifyUnavailable:
		return writeStatus(w, StatusUnavailable)
	}
	d, token, err := a.devices.Create(ctx, user, deviceName, password)
	if errors.Is(err, ErrTooManyDevices) {
		return writeStatus(w, StatusTooManyDevices)
	}
	if err != nil {
		return writeStatus(w, StatusUnavailable)
	}
	srv.authenticate(sess, d, password, now)
	return credentialResponse(w, d, token)
}

func (a *Accounts) resume(ctx context.Context, srv *Server, sess *Session, body []byte, w io.Writer) error {
	if len(body) != 32 {
		return writeStatus(w, StatusBadRequest)
	}
	now := a.now()
	if !a.ips.allow(sess.ip, now) {
		return writeStatus(w, StatusRateLimited)
	}
	token := [32]byte(body)
	d, password, err := a.devices.Peek(ctx, token)
	switch {
	case errors.Is(err, ErrTokenReused):
		a.revoked(d.ID)
		return writeStatus(w, StatusRevoked)
	case errors.Is(err, ErrDeviceUnknown):
		return writeStatus(w, StatusRevoked)
	case err != nil:
		return writeStatus(w, StatusUnavailable)
	}
	// Пароль сверяется до обновления токена: смена пароля или удаление пользователя в Navidrome отзывает все его устройства.
	switch a.verifier.Verify(ctx, d.Username, password) {
	case VerifyRejected:
		if ids, err := a.devices.RevokeUser(ctx, d.Username); err == nil {
			a.revoked(ids...)
		}
		return writeStatus(w, StatusRevoked)
	case VerifyUnavailable:
		return writeStatus(w, StatusUnavailable)
	}
	d, next, password, err := a.devices.Resume(ctx, token)
	switch {
	case errors.Is(err, ErrTokenReused):
		a.revoked(d.ID)
		return writeStatus(w, StatusRevoked)
	case errors.Is(err, ErrDeviceUnknown):
		return writeStatus(w, StatusRevoked)
	case err != nil:
		return writeStatus(w, StatusUnavailable)
	}
	srv.authenticate(sess, d, password, now)
	return credentialResponse(w, d, next)
}
