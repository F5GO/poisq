package securetransport

import (
	"context"
	"io"
)

// Прикладной формат внутри канала: запрос — op u8 | тело, ответ — status u8 | тело.
// Шаг secure-channel поддерживает только проверочный обмен; вход и Subsonic добавляют свои операции.
const (
	OpPing    byte = 0x01
	OpLogin   byte = 0x02 // u8 len · имя · u16 len · пароль · u8 len · имя устройства
	OpResume  byte = 0x03 // токен устройства [32]
	OpSignOut byte = 0x04 // отзывает устройство этой сессии

	StatusOK             byte = 0x00
	StatusUnknownOp      byte = 0x01
	StatusForbidden      byte = 0x02 // операция требует входа или недоступна в этой сессии
	StatusBadRequest     byte = 0x03
	StatusUnauthorized   byte = 0x04 // неверное имя пользователя или пароль
	StatusRateLimited    byte = 0x05
	StatusUnavailable    byte = 0x06 // Navidrome или вход временно недоступны
	StatusTooManyDevices byte = 0x07
	StatusRevoked        byte = 0x08 // токен устройства отозван или неизвестен: нужен вход заново
)

// PingHandler отвечает на проверочный обмен эхом тела и отклоняет остальные операции.
func PingHandler(_ context.Context, _ *Session, request []byte, w io.Writer) error {
	if len(request) == 0 {
		_, err := w.Write([]byte{StatusBadRequest})
		return err
	}
	if request[0] != OpPing {
		_, err := w.Write([]byte{StatusUnknownOp})
		return err
	}
	if _, err := w.Write([]byte{StatusOK}); err != nil {
		return err
	}
	_, err := w.Write(request[1:])
	return err
}
