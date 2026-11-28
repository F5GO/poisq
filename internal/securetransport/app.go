package securetransport

import (
	"context"
	"io"
)

// Прикладной формат внутри канала: запрос — op u8 | тело, ответ — status u8 | тело.
// Шаг secure-channel поддерживает только проверочный обмен; вход и Subsonic добавляют свои операции.
const (
	OpPing byte = 0x01

	StatusOK         byte = 0x00
	StatusUnknownOp  byte = 0x01
	StatusBadRequest byte = 0x03
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
