package musicmeta

import "testing"

func TestApplyPatchPreservesOmittedAndClearsExplicit(t *testing.T) {
	title := "New"
	rating := 0
	doc := Document{TrackID: "t", Title: "Old", Album: "Album", BPM: 120, Rating: intPtr(80), Genres: []string{"Rock"}}
	got, err := ApplyPatch(doc, MetadataPatch{Title: &title, Rating: &rating, Clear: []Field{FieldBPM}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "New" || got.Album != "Album" || got.BPM != 0 || got.Rating == nil || *got.Rating != 0 || len(got.Genres) != 1 {
		t.Fatalf("unexpected patch: %#v", got)
	}
}

func TestApplyPatchRejectsSetAndClear(t *testing.T) {
	value := 1
	if _, err := ApplyPatch(Document{TrackID: "t"}, MetadataPatch{BPM: &value, Clear: []Field{FieldBPM}}); err == nil {
		t.Fatal("expected conflict")
	}
}

func TestDiffDocumentsAndFingerprint(t *testing.T) {
	a := Document{TrackID: "t", Title: "A", CustomTags: []CustomTag{{Key: "MOOD", Value: "calm"}}}
	b := a.Clone()
	b.Title = "B"
	b.CustomTags[0].Value = "loud"
	changes, err := DiffDocuments(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 || changes[0].Field != "title" || changes[1].Field != "custom:mood" {
		t.Fatalf("changes=%#v", changes)
	}
	fa, _ := DocumentFingerprint(a)
	fb, _ := DocumentFingerprint(b)
	if fa == fb {
		t.Fatal("fingerprints should differ")
	}
}

func TestCustomTagHistoryDistinguishesMissingAndEmpty(t *testing.T) {
	missing := Document{TrackID: "t"}
	empty := Document{TrackID: "t", CustomTags: []CustomTag{{Key: "Mood", Value: ""}}}
	changes, err := DiffDocuments(missing, empty)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].Field != "custom:mood" || string(changes[0].Before) != "null" || string(changes[0].After) != `""` {
		t.Fatalf("unexpected custom diff: %#v", changes)
	}
	restored, err := ApplyFieldValues(missing, []FieldValue{{Field: changes[0].Field, Value: changes[0].After}})
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.CustomTags) != 1 || restored.CustomTags[0].Value != "" {
		t.Fatalf("empty custom tag was not restored: %#v", restored.CustomTags)
	}
	removed, err := ApplyFieldValues(restored, []FieldValue{{Field: changes[0].Field, Value: changes[0].Before}})
	if err != nil || len(removed.CustomTags) != 0 {
		t.Fatalf("missing custom tag was not restored: %#v, %v", removed.CustomTags, err)
	}
}

func intPtr(value int) *int { return &value }
