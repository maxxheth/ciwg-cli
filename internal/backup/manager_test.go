package backup

import (
	"context"
	"fmt"
	"os"
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
