package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestMain(tests *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "--portable-probe" && os.Getenv("ZJG_TEST_CHILD") != "" {
		if os.Getenv("ZJG_TEST_CHILD") == "fail" {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(tests.Run())
}

func TestStageMigrationCopiesAndRedirectsDownloads(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "old", "真果鉴", "真果鉴")
	cache := filepath.Join(root, "old-cache", "真果鉴", "真果鉴")
	external := filepath.Join(root, "videos", "zhenguojian-downloads")
	home := filepath.Join(root, "portable", ".zhenguojian")
	for _, directory := range []string{source, cache, external, home} {
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(source, "shared_preferences.json"), []byte(`{"flutter.profile":"fixture"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(external, "media.mp4"), []byte("synthetic media"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "cached.bin"), []byte("synthetic cache"), 0600); err != nil {
		t.Fatal(err)
	}
	pointer, _ := json.Marshal(map[string]string{"directory": external})
	if err := os.WriteFile(filepath.Join(source, "download-location.json"), pointer, 0600); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(home, "data")
	marker := filepath.Join(home, "migration.pending.json")
	state, err := stageMigration(source, cache, data, filepath.Join(home, "data.migrating"), marker)
	if err != nil {
		t.Fatal(err)
	}
	if state.External != external || state.Cache != cache || state.Cleanup {
		t.Fatalf("invalid migration state: %+v", state)
	}
	if body, err := os.ReadFile(filepath.Join(data, "downloads", "media.mp4")); err != nil || string(body) != "synthetic media" {
		t.Fatalf("download copy: %q, %v", body, err)
	}
	if body, err := os.ReadFile(filepath.Join(data, "platform-cache", "cached.bin")); err != nil || string(body) != "synthetic cache" {
		t.Fatalf("cache copy: %q, %v", body, err)
	}
	if _, err := os.Stat(filepath.Join(source, "shared_preferences.json")); err != nil {
		t.Fatal("source was removed before probe", err)
	}
	newPointer, err := os.ReadFile(filepath.Join(data, "download-location.json"))
	if err != nil {
		t.Fatal(err)
	}
	var location struct {
		Directory string `json:"directory"`
	}
	if json.Unmarshal(newPointer, &location) != nil || location.Directory != filepath.Join(data, "downloads") {
		t.Fatalf("download location was not redirected: %s", newPointer)
	}
	if digest, err := directoryDigest(source); err != nil || digest != state.SourceDigest {
		t.Fatal("source changed during staging", err)
	}
	if err := os.WriteFile(filepath.Join(source, "shared_preferences.json"), []byte(`{"flutter.profile":"changed"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if digest, err := directoryDigest(source); err != nil || digest == state.SourceDigest {
		t.Fatal("source mutation was not detected", err)
	}
}

func TestMigrationKeepsOldFilesUntilProbeSucceeds(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "config", "真果鉴", "真果鉴")
	cache := filepath.Join(root, "cache", "真果鉴", "真果鉴")
	home := filepath.Join(root, "portable", ".zhenguojian")
	for _, directory := range []string{source, cache, home, filepath.Join(home, "tmp")} {
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(source, "shared_preferences.json"), []byte(`{"flutter.profile":"fixture"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "cached.bin"), []byte("synthetic cache"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ZJG_TEST_CHILD", "fail")
	inner, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateLegacyFrom(home, inner, filepath.Join(home, "tmp"), source, cache); err == nil {
		t.Fatal("migration succeeded despite failed probe")
	}
	if _, err := os.Stat(filepath.Join(source, "shared_preferences.json")); err != nil {
		t.Fatal("old data was removed before probe", err)
	}
	if _, err := os.Stat(filepath.Join(cache, "cached.bin")); err != nil {
		t.Fatal("old cache was removed before probe", err)
	}
	if _, err := os.Stat(filepath.Join(home, "migration.pending.json")); err != nil {
		t.Fatal("migration cannot resume", err)
	}
	t.Setenv("ZJG_TEST_CHILD", "success")
	if err := os.WriteFile(filepath.Join(source, "shared_preferences.json"), []byte(`{"flutter.profile":"changed"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := migrateLegacyFrom(home, inner, filepath.Join(home, "tmp"), source, cache); err == nil {
		t.Fatal("migration cleaned a changed source")
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatal("changed source was removed", err)
	}
	if err := os.WriteFile(filepath.Join(source, "shared_preferences.json"), []byte(`{"flutter.profile":"fixture"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := migrateLegacyFrom(home, inner, filepath.Join(home, "tmp"), source, cache); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatal("old data remains after verified migration", err)
	}
	if _, err := os.Stat(cache); !os.IsNotExist(err) {
		t.Fatal("old cache remains after verified migration", err)
	}
	if _, err := os.Stat(filepath.Join(home, "data", "shared_preferences.json")); err != nil {
		t.Fatal("portable data is missing", err)
	}
	if _, err := os.Stat(filepath.Join(home, "migration.pending.json")); !os.IsNotExist(err) {
		t.Fatal("migration marker remains", err)
	}
}

func TestMigrationWithOnlyLegacyCache(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "config", "真果鉴", "真果鉴")
	cache := filepath.Join(root, "cache", "真果鉴", "真果鉴")
	home := filepath.Join(root, "portable", ".zhenguojian")
	for _, directory := range []string{cache, home, filepath.Join(home, "tmp")} {
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(cache, "cached.bin"), []byte("synthetic cache"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ZJG_TEST_CHILD", "success")
	inner, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateLegacyFrom(home, inner, filepath.Join(home, "tmp"), source, cache); err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(filepath.Join(home, "data", "platform-cache", "cached.bin")); err != nil || string(body) != "synthetic cache" {
		t.Fatalf("cache-only copy: %q, %v", body, err)
	}
	if _, err := os.Stat(cache); !os.IsNotExist(err) {
		t.Fatal("old cache remains after verified migration", err)
	}
}
