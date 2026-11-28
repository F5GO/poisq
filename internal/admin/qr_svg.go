package admin

import (
	"strconv"
	"strings"

	"rsc.io/qr"
)

// qrSVG рисует QR-код как SVG из одних модулей: в разметку не попадает ни текст, ни данные запроса.
// Уровень коррекции M, поле вокруг кода — 4 модуля, как требует стандарт.
func qrSVG(text string) (string, error) {
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		return "", err
	}
	const quiet = 4
	size := code.Size + 2*quiet
	var b strings.Builder
	b.Grow(code.Size * code.Size * 6)
	b.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 `)
	b.WriteString(strconv.Itoa(size) + " " + strconv.Itoa(size))
	b.WriteString(`" shape-rendering="crispEdges"><rect width="100%" height="100%" fill="#fff"/><path fill="#000" d="`)
	for y := 0; y < code.Size; y++ {
		for x := 0; x < code.Size; x++ {
			if code.Black(x, y) {
				b.WriteString("M" + strconv.Itoa(x+quiet) + " " + strconv.Itoa(y+quiet) + "h1v1h-1z")
			}
		}
	}
	b.WriteString(`"/></svg>`)
	return b.String(), nil
}
