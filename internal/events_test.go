package internal

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/Muxcore-Media/core/pkg/contracts"
	transcodev1 "github.com/Muxcore-Media/media-transcoder/proto/transcodev1"
)

func TestEventFilePath(t *testing.T) {
	t.Parallel()
	movie, _ := json.Marshal(contracts.MovieFileAddedPayload{FilePath: "/media/movies/a.mkv"})
	if got := eventFilePath(contracts.EventMovieFileAdded, movie); got != filepath.Clean("/media/movies/a.mkv") {
		t.Fatalf("movie path=%q", got)
	}
	tv, _ := json.Marshal(contracts.TVEpisodeFileAddedPayload{FilePath: "/media/tv/ep.mkv"})
	if got := eventFilePath(contracts.EventTVEpisodeFileAdded, tv); got != filepath.Clean("/media/tv/ep.mkv") {
		t.Fatalf("tv path=%q", got)
	}
	dest, _ := json.Marshal(contracts.FileImportedPayload{DestinationPath: "/media/incoming/x.mkv"})
	if got := eventFilePath(contracts.EventFileImported, dest); got != filepath.Clean("/media/incoming/x.mkv") {
		t.Fatalf("import dest=%q", got)
	}
	orig, _ := json.Marshal(contracts.FileImportedPayload{OriginalPath: "/media/orig/x.mkv"})
	if got := eventFilePath(contracts.EventFileImported, orig); got != filepath.Clean("/media/orig/x.mkv") {
		t.Fatalf("import orig=%q", got)
	}
	if got := eventFilePath("unknown", []byte("{}")); got != "" {
		t.Fatalf("unknown=%q", got)
	}
}

func TestOnImportSetupMatching(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	dir := t.TempDir()
	_, err := m.UpsertSetup(ctx, &transcodev1.UpsertSetupRequest{Setup: &transcodev1.TranscodeSetup{
		Name:              "Import HEVC",
		Enabled:           true,
		LibraryPaths:      []string{dir},
		Trigger:           "on_import",
		SourceDisposition: "keep",
		Outputs:           []*transcodev1.SetupOutput{{ProfileId: "h264_fast", Enabled: true}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "movie.mkv")
	matched, err := m.matchSetupsForPath(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if len(matched) != 1 || matched[0].GetTrigger() != "on_import" {
		t.Fatalf("matched=%v", matched)
	}
}
