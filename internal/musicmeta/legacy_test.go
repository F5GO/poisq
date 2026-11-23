package musicmeta

import "testing"

func TestLegacyOverlayPreservesRichFields(t *testing.T) {
	base := Document{
		TrackID: "t", DiscNumber: 2, Genres: []string{"Rock"}, OriginalReleaseDate: PartialDate{Value: "1988-08-15", Precision: PrecisionDay},
		Credits:     []Credit{{Role: RoleArtist, Name: "Old"}, {Role: RoleComposer, Name: "Composer"}},
		ExternalIDs: ExternalIDs{{Type: IDMusicBrainzRecording, Value: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"}},
		FieldStates: map[Field]FieldState{FieldGenres: {Source: SourceManual, Confidence: 1, Locked: true}},
	}
	got := ApplyLegacy(base, LegacyTrack{TrackID: "t", Artist: "A; B", AlbumArtist: "A", Album: "Album", Title: "Title", Year: 1988, TrackNumber: 3})
	if got.DiscNumber != 2 || len(got.Genres) != 1 || got.OriginalReleaseDate.Precision != PrecisionDay || len(got.ExternalIDs) != 1 || !got.FieldStates[FieldGenres].Locked {
		t.Fatalf("rich fields lost: %#v", got)
	}
	if DisplayCredits(got.Credits, RoleArtist) != "A; B" || DisplayCredits(got.Credits, RoleComposer) != "Composer" {
		t.Fatalf("credits not preserved: %#v", got.Credits)
	}
}

func TestLegacyRoundTrip(t *testing.T) {
	legacy := LegacyTrack{TrackID: "t", Artist: "A; B", AlbumArtist: "A", Album: "Album", Title: "Title", Year: 2001, TrackNumber: 4}
	got := ToLegacy(DocumentFromLegacy(legacy))
	if got != legacy {
		t.Fatalf("round trip = %#v, want %#v", got, legacy)
	}
}
