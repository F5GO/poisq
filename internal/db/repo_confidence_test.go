package db

import (
	"path/filepath"
	"testing"
)

func TestConfidenceRepository(t *testing.T) {
	sqlDB, err := OpenPoisqDatabase(filepath.Join(t.TempDir(), "poisq.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	repo := NewConfidenceRepository(sqlDB)

	if rec, err := repo.Get("t1"); err != nil || rec != nil {
		t.Fatalf("для неизвестного трека ожидалось (nil, nil): %v %v", rec, err)
	}
	if err := repo.Set("t1", `[{"text":"a","sec":1.5,"level":"low"}]`, `{"low":1}`); err != nil {
		t.Fatal(err)
	}
	rec, err := repo.Get("t1")
	if err != nil || rec == nil || rec.LinesJSON != `[{"text":"a","sec":1.5,"level":"low"}]` || rec.UpdatedAt == 0 {
		t.Fatalf("Get после Set: %+v %v", rec, err)
	}
	if err := repo.Set("t1", `[]`, ``); err != nil { // перезапись
		t.Fatal(err)
	}
	rec, _ = repo.Get("t1")
	if rec.LinesJSON != "[]" || rec.SummaryJSON != "{}" {
		t.Fatalf("перезапись не сработала: %+v", rec)
	}
	if err := repo.Delete("t1"); err != nil {
		t.Fatal(err)
	}
	if rec, _ := repo.Get("t1"); rec != nil {
		t.Fatal("запись должна быть удалена")
	}
}

func TestCorrections(t *testing.T) {
	sqlDB, err := OpenPoisqDatabase(filepath.Join(t.TempDir(), "poisq.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	repo := NewConfidenceRepository(sqlDB)
	if err := repo.AddCorrections("t1", []Correction{{LineText: "a", AutoSec: 1, CorrectedSec: 1.4, Confidence: "low"}, {LineText: "b", AutoSec: 3, CorrectedSec: 2.9}}); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddCorrections("t2", nil); err != nil {
		t.Fatal(err)
	}
	if n, _ := repo.CountCorrections("t1"); n != 2 {
		t.Fatalf("правок t1: %d", n)
	}
	if n, _ := repo.CountCorrections(""); n != 2 {
		t.Fatalf("всего правок: %d", n)
	}
}
