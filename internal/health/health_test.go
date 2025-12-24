package health

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/metabinary-ltd/storagesentinel/internal/storage"
)

func TestSummaryEmpty(t *testing.T) {
	dir := t.TempDir()
	store, err := storage.Open(dir+"/state.db", slog.Default())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	provider := NewStorageBackedProvider(store, slog.Default())
	report, err := provider.Summary(context.Background())
	if err != nil {
		t.Fatalf("summary err: %v", err)
	}
	if report.Status != "ok" {
		t.Fatalf("expected ok status, got %s", report.Status)
	}
}

func TestMain(m *testing.M) {
	// quiet default logger output
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn})))
	os.Exit(m.Run())
}
