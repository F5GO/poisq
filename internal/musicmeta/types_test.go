package musicmeta

import "testing"

func TestPartialDatePreservesPrecision(t *testing.T) {
	for input, precision := range map[string]DatePrecision{"1988": PrecisionYear, "1988-08": PrecisionMonth, "1988-08-15": PrecisionDay} {
		got, err := ParsePartialDate(input)
		if err != nil || got.Precision != precision || got.Value != input || got.Year() != 1988 {
			t.Fatalf("ParsePartialDate(%q) = %#v, %v", input, got, err)
		}
	}
	for _, input := range []string{"0", "1988/08", "2023-02-29", "2024-13", " 1988 "} {
		if _, err := ParsePartialDate(input); err == nil {
			t.Fatalf("expected invalid date %q", input)
		}
	}
}

func TestDocumentCloneIsDeep(t *testing.T) {
	rating := 80
	doc := Document{TrackID: "t", Rating: &rating, Credits: []Credit{{Role: RoleArtist, Name: "A", ExternalIDs: ExternalIDs{{Type: IDAudioDBArtist, Value: "1"}}}}, Genres: []string{"Rock"}, CustomTags: []CustomTag{{Key: "MOOD", Value: "calm"}}, FieldStates: map[Field]FieldState{FieldTitle: {Source: SourceLegacy}}}
	clone := doc.Clone()
	clone.Credits[0].Name = "B"
	clone.Credits[0].ExternalIDs[0].Value = "2"
	clone.Genres[0] = "Pop"
	clone.CustomTags[0].Value = "loud"
	*clone.Rating = 20
	delete(clone.FieldStates, FieldTitle)
	if doc.Credits[0].Name != "A" || doc.Credits[0].ExternalIDs[0].Value != "1" || doc.Genres[0] != "Rock" || doc.CustomTags[0].Value != "calm" || *doc.Rating != 80 || len(doc.FieldStates) != 1 {
		t.Fatal("Clone aliased mutable data")
	}
}
