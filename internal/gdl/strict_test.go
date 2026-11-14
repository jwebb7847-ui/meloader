package gdl

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/monbooru/monloader/internal/config"
)

func TestStrictBlacklistRefusesDebugDownload(t *testing.T) {
	cfg := config.Default()
	cfg.Tags.StrictBlacklist = true
	runner := New(config.NewProvider(cfg), SiteLists{})
	work := filepath.Join(t.TempDir(), "debug-work")
	if _, _, err := runner.DebugLog(context.Background(), "https://derpibooru.org/images/42", work); err == nil {
		t.Fatal("strict blacklist allowed the debug download")
	}
	if _, err := os.Stat(work); !os.IsNotExist(err) {
		t.Fatalf("debug work directory was touched: %v", err)
	}
}

func TestStrictGalleryDLPreflight(t *testing.T) {
	bin := os.Getenv("MELOADER_STRICT_TEST_GDL")
	if bin == "" {
		t.Skip("set MELOADER_STRICT_TEST_GDL to gallery-dl 1.32.19")
	}
	dir := t.TempDir()
	approved := []Item{{ID: "42", Category: "derpibooru", Subcategory: "search",
		MediaURL: "https://example.invalid/synthetic.jpg",
		Meta:     map[string]any{"id": "42", "category": "derpibooru", "tags": []string{"safe", "rainbow dash"}}}}
	gate, err := strictGate(dir, approved)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyStrictGate(context.Background(), bin, dir, gate); err != nil {
		t.Fatal(err)
	}
	if err := verifyStrictGate(context.Background(), bin, dir, "id in ("); err == nil {
		t.Fatal("malformed predicate passed preflight")
	}
	info, err := os.Stat(filepath.Join(dir, "strict-blacklist.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("manifest permissions = %v", info.Mode())
	}
}
