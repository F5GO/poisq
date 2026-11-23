package db

import (
	"path/filepath"
	"testing"
)

func TestStagingListPageUsesStableCursor(t *testing.T) {
	database, err := OpenPoisqDatabase(filepath.Join(t.TempDir(), "poisq.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repo := NewStagingRepository(database)

	ids := make([]int64, 3)
	for i := range ids {
		ids[i], err = repo.CreateProposal(StagingChangeRecord{
			TargetID: "track-" + string(rune('a'+i)), ChangeType: "manual",
			CurrentJSON: `{}`, ProposedJSON: `{}`,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.Exec(`UPDATE staging_changes SET created_at = 100 WHERE id IN (?, ?, ?)`, ids[0], ids[1], ids[2]); err != nil {
		t.Fatal(err)
	}

	first, err := repo.ListPage("pending", 0, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || first[0].ID != ids[2] || first[1].ID != ids[1] {
		t.Fatalf("unexpected first page: %#v", first)
	}
	second, err := repo.ListPage("pending", first[1].CreatedAt, first[1].ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || second[0].ID != ids[0] {
		t.Fatalf("unexpected second page: %#v", second)
	}
}
