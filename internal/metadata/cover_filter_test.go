// Package metadata отвечает за аудит медиатеки, исправление битой кодировки (CP1251 -> UTF-8 NFC),
// акустическую идентификацию через AcoustID (fpcalc), обогащение через MusicBrainz API и поиск обложек.
//
// Данный файл cover_filter_test.go тестирует фильтрацию караоке и трибьютов.
package metadata

import (
	"testing"
)

func TestCoverFilterRejectsKaraokeAndTributes(t *testing.T) {
	// 1. Караоке-версия чужого артиста для Green Day
	valid, _ := ScoreCoverCandidate(
		"Party Tyme Karaoke",
		"Boulevard of Broken Dreams (Karaoke Version)",
		"Green Day",
		"Boulevard of Broken Dreams",
	)
	if valid {
		t.Fatalf("expected Party Tyme Karaoke to be rejected, got valid")
	}

	// 2. Караоке-лейбл Ameritz
	valid, _ = ScoreCoverCandidate(
		"Karaoke - Ameritz",
		"Boulevard of Broken Dreams",
		"Green Day",
		"Boulevard of Broken Dreams",
	)
	if valid {
		t.Fatalf("expected Karaoke - Ameritz to be rejected, got valid")
	}

	// 3. Подлинный релиз Green Day
	valid, score := ScoreCoverCandidate(
		"Green Day",
		"American Idiot",
		"Green Day",
		"American Idiot",
	)
	if !valid || score != 100 {
		t.Fatalf("expected genuine Green Day release to be accepted with score 100, got valid=%v score=%d", valid, score)
	}

	// 4. Сингл Green Day
	valid, score = ScoreCoverCandidate(
		"Green Day",
		"Boulevard of Broken Dreams - Single",
		"Green Day",
		"Boulevard of Broken Dreams",
	)
	if !valid || score < 80 {
		t.Fatalf("expected genuine Green Day single to be accepted with high score, got valid=%v score=%d", valid, score)
	}
}
