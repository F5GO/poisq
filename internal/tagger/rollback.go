// Package tagger реализует бережное чтение, хирургическую перезапись и откат (Undo)
// аудио-тегов по стандарту MusicBrainz Picard без перекодирования аудиопотока (Bit-Perfect).
//
// Данный файл rollback.go управляет применением предложений из таблицы staging_changes
// с автоматическим сохранением слепка исходных тегов (original_tags_json) и мгновенным
// откатом изменений в один клик (Undo).
package tagger

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/F5GO/poisq/internal/db"
	"github.com/F5GO/poisq/internal/metadata"
	"github.com/F5GO/poisq/internal/musicmeta"
)

// StagingService связывает атомарный теггер, загрузчик обложек и репозитории БД для применения и отката правок.
type StagingService struct {
	writer      *AtomicTagWriter
	coverFetch  *metadata.CoverFetcher
	tracksRepo  *db.TrackRepository
	stagingRepo *db.StagingRepository
	writebacks  *db.WritebackRepository
	metadata    *db.MetadataRepository
	history     *db.MetadataHistoryRepository
	enrichment  *db.MediaEnrichmentRepository
}

// NewStagingService создает новый сервис управления Staging-изменениями и откатами.
func NewStagingService(
	writer *AtomicTagWriter,
	coverFetch *metadata.CoverFetcher,
	tracksRepo *db.TrackRepository,
	stagingRepo *db.StagingRepository,
) *StagingService {
	return &StagingService{
		writer:      writer,
		coverFetch:  coverFetch,
		tracksRepo:  tracksRepo,
		stagingRepo: stagingRepo,
		writebacks:  stagingRepo.WritebackRepository(),
		metadata:    stagingRepo.MetadataRepository(),
		history:     db.NewMetadataHistoryRepository(stagingRepo.Database()),
		enrichment:  stagingRepo.MediaEnrichmentRepository(),
	}
}

// ApplyProposalWithCustomCover применяет предложение, позволяя переопределить URL обложки
// на альтернативный вариант, выбранный пользователем в веб-интерфейсе.
func (s *StagingService) ApplyProposalWithCustomCover(ctx context.Context, proposalID int64, customCoverURL string) error {
	rec, err := s.stagingRepo.GetByID(proposalID)
	if err != nil {
		return fmt.Errorf("предложение #%d не найдено: %w", proposalID, err)
	}
	if rec.Status != "pending" {
		return fmt.Errorf("предложение #%d уже имеет статус %s", proposalID, rec.Status)
	}
	if rec.ChangeType == "nfo_export" {
		var proposal struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal([]byte(rec.ProposedJSON), &proposal); err != nil || proposal.Path == "" || len(proposal.Content) > 1<<20 {
			return fmt.Errorf("invalid NFO export proposal")
		}
		txID, backup, err := s.beginWriteback(ctx, rec, "nfo", proposal.Path, rec.CurrentJSON)
		if err != nil {
			return err
		}
		content := []byte(proposal.Content)
		if err := s.writer.WriteFileAtomically(proposal.Path, content, 1<<20); err != nil {
			return s.rollbackWriteback(ctx, txID, backup, err)
		}
		if err := s.writebacks.UpdateState(ctx, txID, "written", nil); err != nil {
			return s.rollbackWriteback(ctx, txID, backup, err)
		}
		if err := s.writer.VerifyFileContent(proposal.Path, content, 1<<20); err != nil {
			return s.rollbackWriteback(ctx, txID, backup, err)
		}
		if err := s.writebacks.UpdateState(ctx, txID, "verified", nil); err != nil {
			return s.rollbackWriteback(ctx, txID, backup, err)
		}
		if err := s.writebacks.Commit(ctx, txID, rec.ID, rec.CurrentJSON); err != nil {
			return s.rollbackWriteback(ctx, txID, backup, err)
		}
		return s.writebacks.MarkReconciled(ctx, txID)
	}

	if rec.ChangeType == "cover" || rec.ChangeType == "artwork" {
		targetURL := strings.TrimSpace(customCoverURL)
		fileName := "cover.jpg"
		role := "album_primary"
		if targetURL == "" {
			var prop struct {
				CoverURL string `json:"coverUrl"`
				FileName string `json:"fileName"`
				Role     string `json:"role"`
			}
			if err := json.Unmarshal([]byte(rec.ProposedJSON), &prop); err != nil || prop.CoverURL == "" {
				return fmt.Errorf("некорректный URL обложки в предложении #%d", proposalID)
			}
			targetURL = prop.CoverURL
			if prop.FileName != "" {
				fileName = prop.FileName
			}
			if prop.Role != "" {
				role = prop.Role
			}
		} else if rec.ChangeType == "artwork" {
			var prop struct {
				FileName string `json:"fileName"`
				Role     string `json:"role"`
			}
			_ = json.Unmarshal([]byte(rec.ProposedJSON), &prop)
			if prop.FileName != "" {
				fileName = prop.FileName
			}
			if prop.Role != "" {
				role = prop.Role
			}
		}

		txID, backup, err := s.beginWriteback(ctx, rec, "cover", filepath.Join(rec.FolderPath, fileName), rec.CurrentJSON)
		if err != nil {
			return err
		}
		w, h, err := s.coverFetch.DownloadAndSaveRoleAtomically(ctx, rec.FolderPath, targetURL, fileName)
		if err != nil {
			return s.rollbackWriteback(ctx, txID, backup, err)
		}
		if err := s.writebacks.UpdateState(ctx, txID, "written", nil); err != nil {
			return s.rollbackWriteback(ctx, txID, backup, err)
		}
		imageInfo := metadata.DetectImageDimensions(backup.TargetPath)
		if !imageInfo.Valid || imageInfo.Width <= 0 || imageInfo.Height <= 0 {
			return s.rollbackWriteback(ctx, txID, backup, fmt.Errorf("post-write cover verification failed"))
		}
		if err := s.writebacks.UpdateState(ctx, txID, "verified", nil); err != nil {
			return s.rollbackWriteback(ctx, txID, backup, err)
		}
		if err := s.writebacks.Commit(ctx, txID, rec.ID, rec.CurrentJSON); err != nil {
			return s.rollbackWriteback(ctx, txID, backup, err)
		}
		if role == "album_primary" {
			if err := s.tracksRepo.UpdateCoverInfo(rec.FolderPath, true, w, h); err != nil {
				return fmt.Errorf("cover committed but projection reconciliation failed: %w", err)
			}
		}
		if rec.ChangeType == "artwork" {
			if err := s.reconcileArtworkApplied(ctx, rec, backup.TargetPath); err != nil {
				return fmt.Errorf("artwork committed but projection reconciliation failed: %w", err)
			}
		}
		return s.writebacks.MarkReconciled(ctx, txID)
	}

	track, err := s.tracksRepo.GetByID(rec.TargetID)
	if err != nil {
		return fmt.Errorf("трек %s не найден в базе: %w", rec.TargetID, err)
	}

	origSnapshot := CanonicalTagSet{
		Artist:      track.Artist,
		Album:       track.Album,
		AlbumArtist: track.AlbumArtist,
		Title:       track.Title,
		Year:        track.Year,
		TrackNumber: track.TrackNumber,
	}
	origBytes, _ := json.Marshal(origSnapshot)

	var proposed CanonicalTagSet
	if err := json.Unmarshal([]byte(rec.ProposedJSON), &proposed); err != nil {
		return fmt.Errorf("ошибка чтения предлагаемых тегов: %w", err)
	}
	if !proposed.ReplaceAll && proposed.Artist == "" {
		proposed.Artist = track.Artist
	}
	if !proposed.ReplaceAll && proposed.Album == "" {
		proposed.Album = track.Album
	}
	if !proposed.ReplaceAll && proposed.AlbumArtist == "" {
		proposed.AlbumArtist = proposed.Artist
		if rec.ChangeType == "manual" { // ручная правка не добавляет тег, которого не было
			proposed.AlbumArtist = track.AlbumArtist
		}
	}
	if !proposed.ReplaceAll && proposed.Title == "" {
		proposed.Title = track.Title
	}
	if !proposed.ReplaceAll && proposed.Year == 0 {
		proposed.Year = track.Year
	}
	if !proposed.ReplaceAll && proposed.TrackNumber == 0 {
		proposed.TrackNumber = track.TrackNumber
	}
	if err := validateCanonicalTagSet(track.ID, proposed); err != nil {
		return err
	}

	txID, backup, err := s.beginWriteback(ctx, rec, "tags", track.FilePath, string(origBytes))
	if err != nil {
		return err
	}
	if err := s.writer.WriteTagsAtomically(ctx, track.FilePath, proposed); err != nil {
		return s.rollbackWriteback(ctx, txID, backup, err)
	}
	if err := s.writebacks.UpdateState(ctx, txID, "written", nil); err != nil {
		return s.rollbackWriteback(ctx, txID, backup, err)
	}
	if err := s.writer.VerifyTags(ctx, track.FilePath, proposed); err != nil {
		return s.rollbackWriteback(ctx, txID, backup, err)
	}
	if err := s.writebacks.UpdateState(ctx, txID, "verified", nil); err != nil {
		return s.rollbackWriteback(ctx, txID, backup, err)
	}
	if err := s.writebacks.Commit(ctx, txID, rec.ID, string(origBytes)); err != nil {
		return s.rollbackWriteback(ctx, txID, backup, err)
	}
	if err := s.reconcileAppliedTags(ctx, rec, proposed, txID); err != nil {
		return fmt.Errorf("tags committed but projection reconciliation failed: %w", err)
	}
	return s.writebacks.MarkReconciled(ctx, txID)
}

// ApplyManualEdit записывает теги, введённые вручную в Studio. Правка проходит через Staging
// (change_type = manual), поэтому попадает в историю и откатывается так же, как предложения аудита.
func (s *StagingService) ApplyManualEdit(ctx context.Context, trackID string, tags CanonicalTagSet) (int64, error) {
	track, err := s.tracksRepo.GetByID(trackID)
	if err != nil {
		return 0, fmt.Errorf("трек %s не найден в базе: %w", trackID, err)
	}
	currentDoc, err := s.metadata.GetTrackDocument(ctx, trackID)
	if err != nil {
		return 0, fmt.Errorf("read current metadata document: %w", err)
	}
	curJSON, err := json.Marshal(currentDoc)
	if err != nil {
		return 0, fmt.Errorf("encode current metadata document: %w", err)
	}
	tags.Artist = strings.TrimSpace(tags.Artist)
	tags.Album = strings.TrimSpace(tags.Album)
	tags.AlbumArtist = strings.TrimSpace(tags.AlbumArtist)
	tags.Title = strings.TrimSpace(tags.Title)
	proposed := make(map[string]any)
	encodedTags, err := json.Marshal(tags)
	if err != nil {
		return 0, fmt.Errorf("encode manual tags: %w", err)
	}
	if err := json.Unmarshal(encodedTags, &proposed); err != nil {
		return 0, fmt.Errorf("build manual tag proposal: %w", err)
	}
	proposed["reason"] = "Ручная правка в Studio"
	propJSON, err := json.Marshal(proposed)
	if err != nil {
		return 0, fmt.Errorf("encode manual tag proposal: %w", err)
	}
	id, err := s.stagingRepo.CreateProposal(db.StagingChangeRecord{
		Scope:        "track",
		TargetID:     track.ID,
		FolderPath:   track.FolderPath,
		ChangeType:   "manual",
		Confidence:   100,
		CurrentJSON:  string(curJSON),
		ProposedJSON: string(propJSON),
	})
	if err != nil {
		return 0, err
	}
	// При ошибке правка остаётся pending в Staging: файл мог уже измениться, и запись нужна, чтобы повторить или пропустить её
	if err := s.ApplyProposalWithCustomCover(ctx, id, ""); err != nil {
		return 0, err
	}
	return id, nil
}

// StageDocumentEdit creates a pending metadata proposal without touching the audio file.
func (s *StagingService) StageDocumentEdit(ctx context.Context, current, proposed musicmeta.Document, reason string) (int64, error) {
	if current.TrackID == "" || current.TrackID != proposed.TrackID {
		return 0, fmt.Errorf("metadata document identity mismatch")
	}
	clean, err := musicmeta.ValidateDocument(proposed)
	if err != nil {
		return 0, err
	}
	changes, err := musicmeta.DiffDocuments(current, clean)
	if err != nil {
		return 0, err
	}
	if len(changes) == 0 {
		return 0, fmt.Errorf("metadata patch has no changes")
	}
	track, err := s.tracksRepo.GetByID(current.TrackID)
	if err != nil {
		return 0, err
	}
	currentJSON, err := json.Marshal(current)
	if err != nil {
		return 0, err
	}
	canonical := CanonicalFromDocument(clean)
	proposedMap := make(map[string]any)
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return 0, err
	}
	if err := json.Unmarshal(encoded, &proposedMap); err != nil {
		return 0, err
	}
	if strings.TrimSpace(reason) == "" {
		reason = "Ручная правка в Studio"
	}
	proposedMap["reason"] = reason
	proposedJSON, err := json.Marshal(proposedMap)
	if err != nil {
		return 0, err
	}
	return s.stagingRepo.CreateProposal(db.StagingChangeRecord{
		Scope: "track", TargetID: track.ID, FolderPath: track.FolderPath, ChangeType: "manual", Confidence: 100,
		CurrentJSON: string(currentJSON), ProposedJSON: string(proposedJSON),
	})
}

func CanonicalFromDocument(doc musicmeta.Document) CanonicalTagSet {
	value := CanonicalTagSet{
		ReplaceAll: true,
		Artist:     musicmeta.DisplayCredits(doc.Credits, musicmeta.RoleArtist), AlbumArtist: musicmeta.DisplayCredits(doc.Credits, musicmeta.RoleAlbumArtist),
		Album: doc.Album, Title: doc.Title, Subtitle: doc.Subtitle, Year: doc.ReleaseDate.Year(),
		ReleaseDate: doc.ReleaseDate.Value, OriginalDate: doc.OriginalReleaseDate.Value, TrackNumber: doc.TrackNumber,
		TrackTotal: doc.TrackTotal, DiscNumber: doc.DiscNumber, DiscTotal: doc.DiscTotal, Compilation: doc.Compilation,
		Genres: append([]string(nil), doc.Genres...), Label: doc.Label, CatalogNumber: doc.CatalogNumber, Barcode: doc.Barcode,
		ISRC: doc.ISRC, Grouping: doc.Grouping, Comment: doc.Comment, Copyright: doc.Copyright, BPM: doc.BPM,
		ExtraTags: make(map[string]string, len(doc.CustomTags)),
	}
	if doc.Rating != nil {
		rating := *doc.Rating
		value.Rating = &rating
	}
	for _, custom := range doc.CustomTags {
		value.ExtraTags[custom.Key] = custom.Value
	}
	for _, credit := range doc.Credits {
		switch credit.Role {
		case musicmeta.RoleComposer:
			value.Composer = append(value.Composer, credit.Name)
		case musicmeta.RoleLyricist:
			value.Lyricist = append(value.Lyricist, credit.Name)
		case musicmeta.RoleConductor:
			value.Conductor = append(value.Conductor, credit.Name)
		case musicmeta.RolePerformer:
			value.Performer = append(value.Performer, credit.Name)
		case musicmeta.RoleProducer:
			value.Producer = append(value.Producer, credit.Name)
		case musicmeta.RoleRemixer:
			value.Remixer = append(value.Remixer, credit.Name)
		}
	}
	for _, external := range doc.ExternalIDs {
		switch external.Type {
		case musicmeta.IDMusicBrainzRelease:
			value.ReleaseMBID = external.Value
		case musicmeta.IDMusicBrainzReleaseGroup:
			value.ReleaseGroupMBID = external.Value
		case musicmeta.IDMusicBrainzArtist:
			value.ArtistMBID = external.Value
		case musicmeta.IDMusicBrainzRecording:
			value.RecordingMBID = external.Value
		case musicmeta.IDMusicBrainzReleaseTrack:
			value.ReleaseTrackMBID = external.Value
		}
	}
	return value
}

// UndoProposal откатывает ранее примененное изменение тегов, возвращая исходные метаданные из original_tags_json.
func (s *StagingService) UndoProposal(ctx context.Context, proposalID int64) error {
	rec, err := s.stagingRepo.GetByID(proposalID)
	if err != nil {
		return fmt.Errorf("предложение #%d не найдено: %w", proposalID, err)
	}
	if rec.Status != "applied" {
		return fmt.Errorf("откат возможен только для примененных изменений (текущий статус: %s)", rec.Status)
	}
	tx, err := s.writebacks.GetByProposal(ctx, proposalID)
	if err == sql.ErrNoRows {
		return s.undoLegacyProposal(ctx, rec)
	}
	if err != nil {
		return err
	}
	if err := s.writebacks.BeginUndo(ctx, tx.TransactionID, proposalID); err != nil {
		return err
	}
	if err := s.writer.RestoreBackup(backupFromTransaction(tx), tx.TransactionID); err != nil {
		return fmt.Errorf("restore writeback backup: %w", err)
	}
	if err := s.reconcileRestored(ctx, tx, rec); err != nil {
		return fmt.Errorf("original restored but projection reconciliation failed: %w", err)
	}
	return s.writebacks.CompleteUndo(ctx, tx.TransactionID, proposalID)
}

func (s *StagingService) beginWriteback(ctx context.Context, rec *db.StagingChangeRecord, kind, targetPath, originalJSON string) (string, BackupInfo, error) {
	txID, err := newTransactionID()
	if err != nil {
		return "", BackupInfo{}, err
	}
	backup, err := s.writer.PrepareBackup(targetPath, txID)
	if err != nil {
		return "", BackupInfo{}, err
	}
	err = s.writebacks.CreatePrepared(ctx, db.WritebackTransaction{
		TransactionID: txID, ProposalID: rec.ID, Kind: kind, TargetPath: backup.TargetPath,
		BackupPath: backup.BackupPath, OriginalExists: backup.OriginalExists, OriginalSize: backup.OriginalSize,
		OriginalSHA256: backup.OriginalSHA256, OriginalJSON: originalJSON,
	})
	if err != nil {
		if backup.BackupPath != "" {
			_ = os.Remove(backup.BackupPath)
		}
		return "", BackupInfo{}, err
	}
	return txID, backup, nil
}

func (s *StagingService) rollbackWriteback(ctx context.Context, transactionID string, backup BackupInfo, cause error) error {
	_ = s.writebacks.UpdateState(ctx, transactionID, "failed", cause)
	if err := s.writer.RestoreBackup(backup, transactionID); err != nil {
		return fmt.Errorf("%v; automatic restore failed: %w", cause, err)
	}
	_ = s.writebacks.UpdateState(ctx, transactionID, "rolled_back", cause)
	return cause
}

func (s *StagingService) reconcileAppliedTags(ctx context.Context, rec *db.StagingChangeRecord, proposed CanonicalTagSet, transactionID string) error {
	if err := validateCanonicalTagSet(rec.TargetID, proposed); err != nil {
		return err
	}
	doc, err := s.metadata.GetTrackDocument(ctx, rec.TargetID)
	if err != nil {
		return err
	}
	before := doc.Clone()
	if proposed.ReplaceAll {
		doc.Title, doc.Subtitle, doc.Album = proposed.Title, proposed.Subtitle, proposed.Album
		doc.ReleaseDate = canonicalPartialDate(proposed.ReleaseDate, proposed.Year)
		doc.OriginalReleaseDate = canonicalPartialDate(proposed.OriginalDate, 0)
		doc.TrackNumber, doc.TrackTotal, doc.DiscNumber, doc.DiscTotal = proposed.TrackNumber, proposed.TrackTotal, proposed.DiscNumber, proposed.DiscTotal
		doc.Compilation, doc.Genres = proposed.Compilation, append([]string(nil), proposed.Genres...)
		doc.Label, doc.CatalogNumber, doc.Barcode, doc.ISRC = proposed.Label, proposed.CatalogNumber, proposed.Barcode, proposed.ISRC
		doc.Grouping, doc.Comment, doc.Copyright, doc.BPM = proposed.Grouping, proposed.Comment, proposed.Copyright, proposed.BPM
		doc.Rating = nil
		if proposed.Rating != nil {
			value := *proposed.Rating
			doc.Rating = &value
		}
		doc.Credits = nil
		for role, names := range map[musicmeta.CreditRole][]string{
			musicmeta.RoleArtist: splitCanonicalValues(proposed.Artist), musicmeta.RoleAlbumArtist: splitCanonicalValues(proposed.AlbumArtist),
			musicmeta.RoleComposer: proposed.Composer, musicmeta.RoleLyricist: proposed.Lyricist, musicmeta.RoleConductor: proposed.Conductor,
			musicmeta.RolePerformer: proposed.Performer, musicmeta.RoleProducer: proposed.Producer, musicmeta.RoleRemixer: proposed.Remixer,
		} {
			for _, name := range names {
				doc.Credits = append(doc.Credits, musicmeta.Credit{Role: role, Name: name})
			}
		}
		doc.CustomTags = nil
		keys := make([]string, 0, len(proposed.ExtraTags))
		for key := range proposed.ExtraTags {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			doc.CustomTags = append(doc.CustomTags, musicmeta.CustomTag{Key: key, Value: proposed.ExtraTags[key]})
		}
		doc.ExternalIDs = replaceCanonicalExternalIDs(doc.ExternalIDs, canonicalExternalIDs(proposed))
	} else {
		doc.Title = proposed.Title
		doc.Album = proposed.Album
		doc.TrackNumber = proposed.TrackNumber
		if proposed.ReleaseDate != "" || proposed.Year > 0 {
			doc.ReleaseDate = canonicalPartialDate(proposed.ReleaseDate, proposed.Year)
		}
	}
	ids := canonicalExternalIDs(proposed)
	if !proposed.ReplaceAll && len(ids) > 0 {
		doc.ExternalIDs = replaceCanonicalExternalIDs(doc.ExternalIDs, ids)
	}
	if !proposed.ReplaceAll && proposed.Subtitle != "" {
		doc.Subtitle = proposed.Subtitle
	}
	if !proposed.ReplaceAll && proposed.TrackTotal > 0 {
		doc.TrackTotal = proposed.TrackTotal
	}
	if !proposed.ReplaceAll && proposed.DiscNumber > 0 {
		doc.DiscNumber = proposed.DiscNumber
	}
	if !proposed.ReplaceAll && proposed.DiscTotal > 0 {
		doc.DiscTotal = proposed.DiscTotal
	}
	if !proposed.ReplaceAll && proposed.Compilation {
		doc.Compilation = true
	}
	if !proposed.ReplaceAll && proposed.BPM > 0 {
		doc.BPM = proposed.BPM
	}
	if !proposed.ReplaceAll && proposed.Rating != nil {
		value := *proposed.Rating
		doc.Rating = &value
	}
	if !proposed.ReplaceAll && len(proposed.Genres) > 0 {
		doc.Genres = append([]string(nil), proposed.Genres...)
	}
	roles := map[musicmeta.CreditRole][]string{
		musicmeta.RoleArtist: splitCanonicalValues(proposed.Artist), musicmeta.RoleAlbumArtist: splitCanonicalValues(proposed.AlbumArtist),
		musicmeta.RoleComposer: proposed.Composer, musicmeta.RoleLyricist: proposed.Lyricist,
		musicmeta.RoleConductor: proposed.Conductor, musicmeta.RolePerformer: proposed.Performer,
		musicmeta.RoleProducer: proposed.Producer, musicmeta.RoleRemixer: proposed.Remixer,
	}
	if !proposed.ReplaceAll {
		for role, names := range roles {
			if len(names) == 0 {
				continue
			}
			kept := doc.Credits[:0]
			for _, credit := range doc.Credits {
				if credit.Role != role {
					kept = append(kept, credit)
				}
			}
			doc.Credits = kept
			for _, name := range names {
				doc.Credits = append(doc.Credits, musicmeta.Credit{Role: role, Name: name})
			}
		}
	}
	if !proposed.ReplaceAll && proposed.ExtraTags != nil {
		doc.CustomTags = doc.CustomTags[:0]
		keys := make([]string, 0, len(proposed.ExtraTags))
		for key := range proposed.ExtraTags {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			doc.CustomTags = append(doc.CustomTags, musicmeta.CustomTag{Key: key, Value: proposed.ExtraTags[key]})
		}
	}
	if !proposed.ReplaceAll {
		for _, item := range []struct {
			value  string
			target *string
		}{
			{proposed.Label, &doc.Label}, {proposed.CatalogNumber, &doc.CatalogNumber}, {proposed.Barcode, &doc.Barcode},
			{proposed.ISRC, &doc.ISRC}, {proposed.Grouping, &doc.Grouping}, {proposed.Comment, &doc.Comment}, {proposed.Copyright, &doc.Copyright},
		} {
			if item.value != "" {
				*item.target = item.value
			}
		}
	}
	source := musicmeta.SourceManual
	provider := "studio"
	if rec.ChangeType == "musicbrainz" || rec.ChangeType == "artist_canonical" {
		source = musicmeta.SourceMusicBrainz
		provider = "musicbrainz"
	}
	confidence := float64(rec.Confidence) / 100
	if confidence < 0 {
		confidence = 0
	}
	if confidence > 1 {
		confidence = 1
	}
	changes, err := musicmeta.DiffDocuments(before, doc)
	if err != nil {
		return err
	}
	state := musicmeta.FieldState{Source: source, Provider: provider, Confidence: confidence, UpdatedAt: time.Now().UTC()}
	for _, change := range changes {
		field := musicmeta.Field(change.Field)
		if strings.HasPrefix(change.Field, "custom:") {
			field = musicmeta.FieldCustomTags
		}
		doc.FieldStates[field] = state
	}
	if err := s.metadata.PutTrackDocument(ctx, doc); err != nil {
		return err
	}
	after, err := s.metadata.GetTrackDocument(ctx, rec.TargetID)
	if err != nil {
		return err
	}
	changes, err = musicmeta.DiffDocuments(before, after)
	if err != nil {
		return err
	}
	if len(changes) == 0 {
		return nil
	}
	actor := "system"
	if source == musicmeta.SourceManual {
		actor = "studio"
	}
	if err := s.history.Append(ctx, rec.TargetID, rec.ID, transactionID, string(source), provider, actor, changes); err != nil {
		return err
	}
	return s.history.CompleteLinkedRevert(ctx, rec.ID)
}

func validateCanonicalTagSet(trackID string, tags CanonicalTagSet) error {
	credits := []musicmeta.Credit{}
	for role, names := range map[musicmeta.CreditRole][]string{
		musicmeta.RoleArtist: splitCanonicalValues(tags.Artist), musicmeta.RoleAlbumArtist: splitCanonicalValues(tags.AlbumArtist),
		musicmeta.RoleComposer: tags.Composer, musicmeta.RoleLyricist: tags.Lyricist, musicmeta.RoleConductor: tags.Conductor,
		musicmeta.RolePerformer: tags.Performer, musicmeta.RoleProducer: tags.Producer, musicmeta.RoleRemixer: tags.Remixer,
	} {
		for _, name := range names {
			credits = append(credits, musicmeta.Credit{Role: role, Name: name})
		}
	}
	custom := make([]musicmeta.CustomTag, 0, len(tags.ExtraTags))
	for key, value := range tags.ExtraTags {
		custom = append(custom, musicmeta.CustomTag{Key: key, Value: value})
	}
	doc := musicmeta.Document{
		TrackID: trackID, Title: tags.Title, Subtitle: tags.Subtitle, Album: tags.Album,
		TrackNumber: tags.TrackNumber, TrackTotal: tags.TrackTotal, DiscNumber: tags.DiscNumber, DiscTotal: tags.DiscTotal,
		Compilation: tags.Compilation, Genres: tags.Genres, Label: tags.Label, CatalogNumber: tags.CatalogNumber,
		Barcode: tags.Barcode, ISRC: tags.ISRC, Grouping: tags.Grouping, Comment: tags.Comment,
		Copyright: tags.Copyright, ExternalIDs: canonicalExternalIDs(tags),
		BPM: tags.BPM, Rating: tags.Rating, Credits: credits, CustomTags: custom,
	}
	if tags.ReleaseDate != "" || tags.Year > 0 {
		doc.ReleaseDate = canonicalPartialDate(tags.ReleaseDate, tags.Year)
	}
	if tags.OriginalDate != "" {
		doc.OriginalReleaseDate = canonicalPartialDate(tags.OriginalDate, 0)
	}
	_, err := musicmeta.ValidateDocument(doc)
	if err != nil {
		return fmt.Errorf("invalid canonical tags: %w", err)
	}
	return nil
}

func canonicalPartialDate(value string, fallbackYear int) musicmeta.PartialDate {
	if strings.TrimSpace(value) != "" {
		parsed, err := musicmeta.ParsePartialDate(strings.TrimSpace(value))
		if err == nil {
			return parsed
		}
	}
	if fallbackYear > 0 {
		return musicmeta.PartialDate{Value: fmt.Sprintf("%04d", fallbackYear), Precision: musicmeta.PrecisionYear}
	}
	return musicmeta.PartialDate{}
}

func splitCanonicalValues(value string) []string {
	parts := strings.Split(value, ";")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if clean := strings.TrimSpace(part); clean != "" {
			out = append(out, clean)
		}
	}
	return out
}

func canonicalExternalIDs(tags CanonicalTagSet) musicmeta.ExternalIDs {
	values := []struct {
		kind  musicmeta.ExternalIDType
		value string
	}{
		{musicmeta.IDMusicBrainzRelease, tags.ReleaseMBID},
		{musicmeta.IDMusicBrainzReleaseGroup, tags.ReleaseGroupMBID},
		{musicmeta.IDMusicBrainzArtist, tags.ArtistMBID},
		{musicmeta.IDMusicBrainzRecording, tags.RecordingMBID},
		{musicmeta.IDMusicBrainzReleaseTrack, tags.ReleaseTrackMBID},
	}
	var out musicmeta.ExternalIDs
	for _, value := range values {
		if strings.TrimSpace(value.value) != "" {
			out = append(out, musicmeta.ExternalID{Type: value.kind, Value: strings.TrimSpace(value.value)})
		}
	}
	return out
}

func replaceCanonicalExternalIDs(current, replacement musicmeta.ExternalIDs) musicmeta.ExternalIDs {
	canonical := map[musicmeta.ExternalIDType]bool{
		musicmeta.IDMusicBrainzRelease: true, musicmeta.IDMusicBrainzReleaseGroup: true,
		musicmeta.IDMusicBrainzArtist: true, musicmeta.IDMusicBrainzRecording: true, musicmeta.IDMusicBrainzReleaseTrack: true,
	}
	out := make(musicmeta.ExternalIDs, 0, len(current)+len(replacement))
	for _, value := range current {
		if !canonical[value.Type] {
			out = append(out, value)
		}
	}
	return append(out, replacement...)
}

func (s *StagingService) reconcileRestored(ctx context.Context, tx db.WritebackTransaction, rec *db.StagingChangeRecord) error {
	if tx.Kind == "nfo" {
		return nil
	}
	if tx.Kind == "cover" {
		if rec.ChangeType == "artwork" {
			return s.reconcileArtworkRestored(ctx, rec)
		}
		has, width, height, _ := s.coverFetch.InspectFolderCover(rec.FolderPath)
		return s.tracksRepo.UpdateCoverInfo(rec.FolderPath, has, width, height)
	}
	current, err := s.metadata.GetTrackDocument(ctx, rec.TargetID)
	if err != nil {
		return err
	}
	current.ExternalIDs = replaceCanonicalExternalIDs(current.ExternalIDs, nil)
	probed, probeErr := metadata.NewFFProbeAudioProber("").Probe(ctx, tx.TargetPath, rec.TargetID)
	if probeErr == nil {
		policy := musicmeta.MergePolicy{Rules: []musicmeta.ProviderRule{
			{Source: musicmeta.SourceAudioFile, Priority: 0, RefreshMode: musicmeta.RefreshPriority, Enabled: true},
			{Source: musicmeta.SourceManual, Priority: 10, RefreshMode: musicmeta.RefreshPriority, Enabled: true},
			{Source: musicmeta.SourceMusicBrainz, Priority: 20, RefreshMode: musicmeta.RefreshPriority, Enabled: true},
			{Source: musicmeta.SourceLegacy, Priority: 30, RefreshMode: musicmeta.RefreshPriority, Enabled: true},
		}}
		if merged, mergeErr := musicmeta.Merge(current, []musicmeta.Document{probed.Document}, policy); mergeErr == nil {
			return s.metadata.PutTrackDocument(ctx, merged)
		}
	}
	var original CanonicalTagSet
	if err := json.Unmarshal([]byte(tx.OriginalJSON), &original); err != nil {
		return err
	}
	_, mojArtist := metadata.DetectAndFixMojibake(original.Artist)
	_, mojTitle := metadata.DetectAndFixMojibake(original.Title)
	return s.tracksRepo.UpdateMetadataFull(rec.TargetID, original.Artist, original.Album, original.AlbumArtist, original.Title, original.Year, original.TrackNumber, mojArtist || mojTitle)
}

func backupFromTransaction(tx db.WritebackTransaction) BackupInfo {
	return BackupInfo{TargetPath: tx.TargetPath, BackupPath: tx.BackupPath, OriginalExists: tx.OriginalExists, OriginalSize: tx.OriginalSize, OriginalSHA256: tx.OriginalSHA256}
}

func newTransactionID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate writeback transaction id: %w", err)
	}
	return hex.EncodeToString(value), nil
}

func (s *StagingService) undoLegacyProposal(ctx context.Context, rec *db.StagingChangeRecord) error {
	if rec.ChangeType == "cover" {
		return fmt.Errorf("legacy cover proposal has no byte backup")
	}
	track, err := s.tracksRepo.GetByID(rec.TargetID)
	if err != nil {
		return err
	}
	var original, proposed CanonicalTagSet
	if err := json.Unmarshal([]byte(rec.OriginalTagsJSON), &original); err != nil {
		return err
	}
	_ = json.Unmarshal([]byte(rec.ProposedJSON), &proposed)
	original.Clear = undoClears(original, proposed)
	if err := s.writer.WriteTagsAtomically(ctx, track.FilePath, original); err != nil {
		return err
	}
	return s.stagingRepo.MarkStatus(rec.ID, "reverted")
}

func (s *StagingService) RecoverIncomplete(ctx context.Context) error {
	incomplete, err := s.writebacks.ListIncomplete(ctx)
	if err != nil {
		return err
	}
	for _, tx := range incomplete {
		rec, err := s.stagingRepo.GetByID(tx.ProposalID)
		if err != nil {
			return err
		}
		if err := s.writer.RestoreBackup(backupFromTransaction(tx), tx.TransactionID); err != nil {
			return err
		}
		if err := s.reconcileRestored(ctx, tx, rec); err != nil {
			return err
		}
		if tx.State == "undoing" {
			if err := s.writebacks.CompleteUndo(ctx, tx.TransactionID, tx.ProposalID); err != nil {
				return err
			}
		} else if err := s.writebacks.UpdateState(ctx, tx.TransactionID, "rolled_back", fmt.Errorf("recovered incomplete writeback")); err != nil {
			return err
		}
	}
	committed, err := s.writebacks.ListCommittedUnreconciled(ctx)
	if err != nil {
		return err
	}
	for _, tx := range committed {
		rec, err := s.stagingRepo.GetByID(tx.ProposalID)
		if err != nil {
			return err
		}
		if tx.Kind == "nfo" {
			// The file commit is already durable; no database projection is required.
		} else if tx.Kind == "cover" {
			if rec.ChangeType == "artwork" {
				if err := s.reconcileArtworkApplied(ctx, rec, tx.TargetPath); err != nil {
					return err
				}
			} else {
				has, width, height, _ := s.coverFetch.InspectFolderCover(rec.FolderPath)
				if err := s.tracksRepo.UpdateCoverInfo(rec.FolderPath, has, width, height); err != nil {
					return err
				}
			}
		} else {
			var proposed CanonicalTagSet
			if err := json.Unmarshal([]byte(rec.ProposedJSON), &proposed); err != nil {
				return err
			}
			if err := s.reconcileAppliedTags(ctx, rec, proposed, tx.TransactionID); err != nil {
				return err
			}
		}
		if err := s.writebacks.MarkReconciled(ctx, tx.TransactionID); err != nil {
			return err
		}
	}
	return nil
}

type stagedArtworkProjection struct {
	Role    string `json:"role"`
	Scope   string `json:"scope"`
	ScopeID string `json:"scopeId"`
	Source  string `json:"source"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
	Score   int    `json:"score"`
}

func (s *StagingService) reconcileArtworkApplied(ctx context.Context, rec *db.StagingChangeRecord, filePath string) error {
	var value stagedArtworkProjection
	if err := json.Unmarshal([]byte(rec.ProposedJSON), &value); err != nil {
		return fmt.Errorf("decode artwork projection: %w", err)
	}
	return s.enrichment.PutArtwork(ctx, db.ArtworkRecord{
		Scope: value.Scope, ScopeID: value.ScopeID, Role: value.Role, FilePath: filePath,
		Source: value.Source, Width: value.Width, Height: value.Height, Score: value.Score,
	})
}

func (s *StagingService) reconcileArtworkRestored(ctx context.Context, rec *db.StagingChangeRecord) error {
	var proposal stagedArtworkProjection
	if err := json.Unmarshal([]byte(rec.ProposedJSON), &proposal); err != nil {
		return fmt.Errorf("decode artwork proposal: %w", err)
	}
	var snapshot struct {
		Previous *db.ArtworkRecord `json:"previous"`
	}
	if err := json.Unmarshal([]byte(rec.CurrentJSON), &snapshot); err != nil {
		return fmt.Errorf("decode artwork snapshot: %w", err)
	}
	if snapshot.Previous != nil {
		return s.enrichment.PutArtwork(ctx, *snapshot.Previous)
	}
	return s.enrichment.DeleteArtwork(ctx, proposal.Scope, proposal.ScopeID, proposal.Role)
}

// IgnoreProposal переводит предложение в статус ignored.
func (s *StagingService) IgnoreProposal(proposalID int64) error {
	return s.stagingRepo.MarkStatus(proposalID, "ignored")
}
