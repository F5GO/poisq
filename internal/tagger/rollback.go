// Package tagger реализует бережное чтение, хирургическую перезапись и откат (Undo)
// аудио-тегов по стандарту MusicBrainz Picard без перекодирования аудиопотока (Bit-Perfect).
//
// Данный файл rollback.go управляет применением предложений из таблицы staging_changes
// с автоматическим сохранением слепка исходных тегов (original_tags_json) и мгновенным
// откатом изменений в один клик (Undo).
package tagger

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/F5GO/poisq/internal/db"
	"github.com/F5GO/poisq/internal/metadata"
)

// StagingService связывает атомарный теггер, загрузчик обложек и репозитории БД для применения и отката правок.
type StagingService struct {
	writer      *AtomicTagWriter
	coverFetch  *metadata.CoverFetcher
	tracksRepo  *db.TrackRepository
	stagingRepo *db.StagingRepository
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

	// Кейс 1: Скачивание и установка обложки альбома (scope = folder, change_type = cover)
	if rec.ChangeType == "cover" {
		targetURL := strings.TrimSpace(customCoverURL)
		if targetURL == "" {
			var prop struct {
				CoverURL string `json:"coverUrl"`
			}
			if err := json.Unmarshal([]byte(rec.ProposedJSON), &prop); err != nil || prop.CoverURL == "" {
				return fmt.Errorf("некорректный URL обложки в предложении #%d", proposalID)
			}
			targetURL = prop.CoverURL
		}

		w, h, err := s.coverFetch.DownloadAndSaveAtomically(ctx, rec.FolderPath, targetURL)
		if err != nil {
			return err
		}
		_ = s.tracksRepo.UpdateCoverInfo(rec.FolderPath, true, w, h)
		return s.stagingRepo.MarkApplied(proposalID, rec.CurrentJSON)
	}

	// Кейс 2: Хирургическое обновление тегов трека (mojibake, musicbrainz, artist_canonical)
	track, err := s.tracksRepo.GetByID(rec.TargetID)
	if err != nil {
		return fmt.Errorf("трек %s не найден в базе: %w", rec.TargetID, err)
	}

	// Сохраняем слепок исходных тегов для Undo
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
	if proposed.Artist == "" {
		proposed.Artist = track.Artist
	}
	if proposed.Album == "" {
		proposed.Album = track.Album
	}
	if proposed.AlbumArtist == "" {
		proposed.AlbumArtist = proposed.Artist
	}
	if proposed.Title == "" {
		proposed.Title = track.Title
	}
	if proposed.Year == 0 {
		proposed.Year = track.Year
	}
	if proposed.TrackNumber == 0 {
		proposed.TrackNumber = track.TrackNumber
	}

	// Атомарно записываем теги в аудиофайл по стандарту Picard (если файл физически доступен)
	if err := s.writer.WriteTagsAtomically(ctx, track.FilePath, proposed); err != nil {
		return err
	}

	// Обновляем запись трека в poisq.db
	if err := s.tracksRepo.UpdateMetadata(track.ID, proposed.Artist, proposed.Album, proposed.AlbumArtist, proposed.Title, proposed.Year, false); err != nil {
		return err
	}

	return s.stagingRepo.MarkApplied(proposalID, string(origBytes))
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
	if rec.ChangeType == "cover" {
		return s.stagingRepo.MarkStatus(proposalID, "reverted")
	}

	track, err := s.tracksRepo.GetByID(rec.TargetID)
	if err != nil {
		return fmt.Errorf("трек %s не найден: %w", rec.TargetID, err)
	}

	var orig CanonicalTagSet
	if err := json.Unmarshal([]byte(rec.OriginalTagsJSON), &orig); err != nil {
		return fmt.Errorf("не удалось прочитать слепок исходных тегов для Undo: %w", err)
	}
	var proposed CanonicalTagSet
	_ = json.Unmarshal([]byte(rec.ProposedJSON), &proposed) // без правки нечего убирать — откат просто вернёт исходные значения
	orig.Clear = undoClears(orig, proposed)

	if err := s.writer.WriteTagsAtomically(ctx, track.FilePath, orig); err != nil {
		return err
	}

	_, isMojArtist := metadata.DetectAndFixMojibake(orig.Artist)
	_, isMojTitle := metadata.DetectAndFixMojibake(orig.Title)
	_ = s.tracksRepo.UpdateMetadata(track.ID, orig.Artist, orig.Album, orig.AlbumArtist, orig.Title, orig.Year, isMojArtist || isMojTitle)

	return s.stagingRepo.MarkStatus(proposalID, "reverted")
}

// IgnoreProposal переводит предложение в статус ignored.
func (s *StagingService) IgnoreProposal(proposalID int64) error {
	return s.stagingRepo.MarkStatus(proposalID, "ignored")
}
