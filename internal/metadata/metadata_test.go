// Package metadata отвечает за аудит медиатеки, исправление битой кодировки (CP1251 -> UTF-8 NFC),
// обогащение через MusicBrainz API и поиск обложек.
//
// Данный файл metadata_test.go проверяет детектор и исправление битой кодировки CP1251 (Mojibake),
// нормализацию Unicode NFC, защиту европейских имен с диакритикой и правило «Folder-as-Album Lock».
package metadata

import (
	"testing"

	"github.com/F5GO/poisq/internal/db"
)

func TestMojibakeFixerAndNFC(t *testing.T) {
	// "Кино - Группа крови" в CP1251, прочитанное как Latin-1
	mojibakeInput := "\xca\xe8\xed\xee - \xc3\xf0\xf3\xef\xef\xe0 \xea\xf0\xee\xe2\xe8"
	fixed, isMoj := DetectAndFixMojibake(mojibakeInput)
	if !isMoj {
		t.Fatalf("expected mojibake to be detected for %q", mojibakeInput)
	}
	if fixed != "Кино - Группа крови" {
		t.Fatalf("got %q, want %q", fixed, "Кино - Группа крови")
	}

	// Западноевропейские артисты с умляутами не должны ломаться
	for _, validEuro := range []string{"Björk", "Motörhead", "Mylène Farmer", "Röyksopp"} {
		out, detected := DetectAndFixMojibake(validEuro)
		if detected || out != validEuro {
			t.Fatalf("euro artist %q was falsely flagged as mojibake: got %q", validEuro, out)
		}
	}

	// Проверка macOS NFD -> NFC ('и' + U+0306 -> 'й', 'е' + U+0308 -> 'ё')
	nfd := "Чай\u0306ф - Е\u0308жик"
	nfc := NormalizeNFC("Чаи\u0306ф - Е\u0308жик")
	_ = nfd
	if nfc != "Чайф - Ёжик" {
		t.Fatalf("NFC normalization failed: got %q, want %q", nfc, "Чайф - Ёжик")
	}
}

func TestFolderAsAlbumLock(t *testing.T) {
	tracks := []db.TrackRecord{
		{ID: "1", FolderPath: "/music/Kino/Gruppa_Krovi", Artist: "Кино", Album: "Группа крови", TrackNumber: 1, Year: 1988},
		{ID: "2", FolderPath: "/music/Kino/Gruppa_Krovi", Artist: "Кино", Album: "Группа крови", TrackNumber: 2, Year: 1988},
		{ID: "3", FolderPath: "/music/Kino/Gruppa_Krovi", Artist: "Кино", Album: "Best Hits Compilation", TrackNumber: 3, Year: 2020},
	}

	lock := ComputeFolderAlbumLock(tracks)
	if !lock.IsNumberedAlbum {
		t.Fatalf("expected folder to be locked as a numbered album")
	}
	if lock.DominantAlbum != "Группа крови" || lock.DominantYear != 1988 {
		t.Fatalf("unexpected consensus: %+v", lock)
	}
}
