package backup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
)

// These tests are integration-style and will only run when the following
// environment variables are set:
// MINIO_TEST_ENDPOINT, MINIO_TEST_ACCESS_KEY, MINIO_TEST_SECRET_KEY, MINIO_TEST_BUCKET
// They are skipped by default to avoid requiring a Minio server in CI.

func getTestMinioConfigFromEnv() *MinioConfig {
	endpoint := os.Getenv("MINIO_TEST_ENDPOINT")
	access := os.Getenv("MINIO_TEST_ACCESS_KEY")
	secret := os.Getenv("MINIO_TEST_SECRET_KEY")
	bucket := os.Getenv("MINIO_TEST_BUCKET")
	useSSL := true
	if os.Getenv("MINIO_TEST_SSL") == "false" {
		useSSL = false
	}

	if endpoint == "" || access == "" || secret == "" || bucket == "" {
		return nil
	}

	return &MinioConfig{
		Endpoint:  endpoint,
		AccessKey: access,
		SecretKey: secret,
		Bucket:    bucket,
		UseSSL:    useSSL,
	}
}

func TestSanitizeBackup(t *testing.T) {
	// Create a temporary directory for test files
	tmpDir, err := os.MkdirTemp("", "test-sanitize-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create test backup structure
	backupDir := filepath.Join(tmpDir, "test-backup")
	wpContentDir := filepath.Join(backupDir, "site", "www", "wp-content")
	themesDir := filepath.Join(wpContentDir, "themes")
	pluginsDir := filepath.Join(wpContentDir, "plugins")
	otherDir := filepath.Join(backupDir, "other-data")

	for _, dir := range []string{themesDir, pluginsDir, otherDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("failed to create dir %s: %v", dir, err)
		}
	}

	// Create test SQL file with license keys
	sqlContent := `-- Test SQL
INSERT INTO wp_options VALUES (1,'license_number','ABC123','yes');
INSERT INTO wp_options VALUES (2,'_elementor_pro_license_data','secret','yes');
INSERT INTO wp_options VALUES (3,'site_url','https://example.com','yes');
INSERT INTO wp_options VALUES (4,'_transient_rg_gforms_license','key123','yes');
INSERT INTO wp_options VALUES (5,'blogname','Test Site','yes');
`
	sqlFile := filepath.Join(wpContentDir, "database.sql")
	if err := os.WriteFile(sqlFile, []byte(sqlContent), 0644); err != nil {
		t.Fatalf("failed to write SQL file: %v", err)
	}

	// Create test theme and plugin files
	if err := os.WriteFile(filepath.Join(themesDir, "theme.php"), []byte("<?php // theme"), 0644); err != nil {
		t.Fatalf("failed to write theme file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginsDir, "plugin.php"), []byte("<?php // plugin"), 0644); err != nil {
		t.Fatalf("failed to write plugin file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(otherDir, "data.txt"), []byte("other data"), 0644); err != nil {
		t.Fatalf("failed to write other file: %v", err)
	}

	// Create tarball
	inputTarball := filepath.Join(tmpDir, "test-backup.tgz")
	bm := NewBackupManager(nil, nil)
	if err := bm.createTarball(backupDir, inputTarball); err != nil {
		t.Fatalf("failed to create test tarball: %v", err)
	}

	// Test sanitization
	outputTarball := filepath.Join(tmpDir, "sanitized-backup.tgz")
	options := &SanitizeOptions{
		InputPath:    inputTarball,
		OutputPath:   outputTarball,
		ExtractDirs:  []string{"wp-content"},
		ExtractFiles: []string{"*.sql"},
		DryRun:       false,
	}

	if err := bm.SanitizeBackup(options); err != nil {
		t.Fatalf("sanitization failed: %v", err)
	}

	// Verify output tarball exists
	if _, err := os.Stat(outputTarball); os.IsNotExist(err) {
		t.Fatalf("output tarball was not created")
	}

	// Extract and verify sanitized content
	extractDir := filepath.Join(tmpDir, "extracted")
	if err := os.MkdirAll(extractDir, 0755); err != nil {
		t.Fatalf("failed to create extract dir: %v", err)
	}
	if err := bm.extractTarball(outputTarball, extractDir); err != nil {
		t.Fatalf("failed to extract sanitized tarball: %v", err)
	}

	// Verify wp-content files are present
	sanitizedSQL := filepath.Join(extractDir, "site", "www", "wp-content", "database.sql")
	if _, err := os.Stat(sanitizedSQL); os.IsNotExist(err) {
		t.Fatalf("SQL file not found in sanitized backup")
	}

	// Verify theme and plugin files are present
	sanitizedTheme := filepath.Join(extractDir, "site", "www", "wp-content", "themes", "theme.php")
	if _, err := os.Stat(sanitizedTheme); os.IsNotExist(err) {
		t.Fatalf("theme file not found in sanitized backup")
	}

	sanitizedPlugin := filepath.Join(extractDir, "site", "www", "wp-content", "plugins", "plugin.php")
	if _, err := os.Stat(sanitizedPlugin); os.IsNotExist(err) {
		t.Fatalf("plugin file not found in sanitized backup")
	}

	// Verify other-data directory is NOT present
	sanitizedOther := filepath.Join(extractDir, "other-data", "data.txt")
	if _, err := os.Stat(sanitizedOther); !os.IsNotExist(err) {
		t.Fatalf("other-data should not be in sanitized backup")
	}

	// Verify license keys were removed from SQL
	sanitizedContent, err := os.ReadFile(sanitizedSQL)
	if err != nil {
		t.Fatalf("failed to read sanitized SQL: %v", err)
	}

	sanitizedStr := string(sanitizedContent)
	if strings.Contains(sanitizedStr, "license_number") {
		t.Errorf("license_number should be removed from SQL")
	}
	if strings.Contains(sanitizedStr, "_elementor_pro_license_data") {
		t.Errorf("_elementor_pro_license_data should be removed from SQL")
	}
	if strings.Contains(sanitizedStr, "_transient_rg_gforms_license") {
		t.Errorf("_transient_rg_gforms_license should be removed from SQL")
	}

	// Verify non-license data is still present
	if !strings.Contains(sanitizedStr, "site_url") {
		t.Errorf("site_url should still be present in SQL")
	}
	if !strings.Contains(sanitizedStr, "blogname") {
		t.Errorf("blogname should still be present in SQL")
	}
}

func TestSanitizeBackupDryRun(t *testing.T) {
	// Create minimal test structure
	tmpDir, err := os.MkdirTemp("", "test-sanitize-dry-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	backupDir := filepath.Join(tmpDir, "backup")
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		t.Fatalf("failed to create backup dir: %v", err)
	}

	// Create a simple file
	if err := os.WriteFile(filepath.Join(backupDir, "test.txt"), []byte("test"), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	// Create tarball
	inputTarball := filepath.Join(tmpDir, "input.tgz")
	bm := NewBackupManager(nil, nil)
	if err := bm.createTarball(backupDir, inputTarball); err != nil {
		t.Fatalf("failed to create tarball: %v", err)
	}

	// Test dry run
	outputTarball := filepath.Join(tmpDir, "output.tgz")
	options := &SanitizeOptions{
		InputPath:    inputTarball,
		OutputPath:   outputTarball,
		ExtractDirs:  []string{"test-dir"},
		ExtractFiles: []string{"*.sql"},
		DryRun:       true,
	}

	if err := bm.SanitizeBackup(options); err != nil {
		t.Fatalf("dry run failed: %v", err)
	}

	// Verify output was NOT created in dry run mode
	if _, err := os.Stat(outputTarball); !os.IsNotExist(err) {
		t.Errorf("output tarball should not be created in dry run mode")
	}
}

func TestListAndReadRoundTrip(t *testing.T) {
	cfg := getTestMinioConfigFromEnv()
	if cfg == nil {
		t.Skip("Skipping Minio integration test; set MINIO_TEST_ENDPOINT etc to run")
	}

	bm := NewBackupManager(nil, cfg)
	if err := bm.initMinioClient(); err != nil {
		t.Fatalf("failed to init minio client: %v", err)
	}

	ctx := context.Background()
	// create a small test object
	name := fmt.Sprintf("ciwg-cli-test-%d.txt", time.Now().UnixNano())
	content := "hello-ciwg-cli"

	_, err := bm.minioClient.PutObject(ctx, bm.minioConfig.Bucket, name, strings.NewReader(content), int64(len(content)), minio.PutObjectOptions{ContentType: "text/plain"})
	if err != nil {
		t.Fatalf("failed to upload test object: %v", err)
	}
	defer func() {
		_ = bm.minioClient.RemoveObject(ctx, bm.minioConfig.Bucket, name, minio.RemoveObjectOptions{})
	}()

	// Allow some time for object metadata to settle
	time.Sleep(500 * time.Millisecond)

	// List and ensure our object is present
	objs, err := bm.ListBackups("", 100)
	if err != nil {
		t.Fatalf("ListBackups failed: %v", err)
	}

	found := false
	for _, o := range objs {
		if o.Key == name {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("uploaded object %s not found in ListBackups results", name)
	}

	// Read the object to a temp file
	tmp, err := os.CreateTemp("", "ciwg-cli-test-*.txt")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)

	if err := bm.ReadBackup(name, tmpPath); err != nil {
		t.Fatalf("ReadBackup failed: %v", err)
	}

	// Verify file contents
	data, err := os.ReadFile(tmpPath)
	if err != nil {
		t.Fatalf("failed to read downloaded file: %v", err)
	}
	if string(data) != content {
		t.Fatalf("downloaded content mismatch: expected %q got %q", content, string(data))
	}

	// Test GetLatestObject with prefix
	latestKey, err := bm.GetLatestObject("ciwg-cli-test-")
	if err != nil {
		t.Fatalf("GetLatestObject failed: %v", err)
	}
	if latestKey == "" {
		t.Fatalf("GetLatestObject returned empty key")
	}
}
