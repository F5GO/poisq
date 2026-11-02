// Package keypool управляет пулом API-ключей (Gemini, Groq, AcoustID, Yandex, Genius)
// с персистентной памятью в SQLite, равномерным распределением по алгоритму LRU
// и автоматической обработкой лимитов (65 сек cooldown и сброс дневных квот в полночь по PST).
//
// Данный файл quota_clock.go отвечает за точный расчет времени сброса дневной квоты
// (полночь по тихоокеанскому времени America/Los_Angeles / ~10:00-11:00 МСК).
package keypool

import "time"

// NextPSTMidnight вычисляет момент наступления следующей полуночи по тихоокеанскому времени (PST/PDT),
// когда Google Gemini и другие американские облачные провайдеры сбрасывают дневные лимиты (RPD).
func NextPSTMidnight(now time.Time) time.Time {
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		// Фоллбэк на фиксированный сдвиг UTC-8 (PST), если системная база часовых поясов недоступна
		loc = time.FixedZone("PST", -8*3600)
	}

	inPST := now.In(loc)
	nextDay := time.Date(inPST.Year(), inPST.Month(), inPST.Day()+1, 0, 5, 0, 0, loc) // +5 минут запаса после полуночи
	return nextDay.UTC()
}
