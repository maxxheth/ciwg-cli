package dnsbackup

import (
	"testing"
	"time"
)

func TestExtractZoneName(t *testing.T) {
	tests := []struct {
		name     string
		key      string
		expected string
	}{
		{
			name:     "standard format",
			key:      "dns-backups/example.com-20240101-120000.json",
			expected: "example.com",
		},
		{
			name:     "subdomain",
			key:      "dns-backups/api.example.com-20240101-120000.json",
			expected: "api.example.com",
		},
		{
			name:     "with bucket path",
			key:      "production/dns-backups/example.com-20240101-120000.yaml",
			expected: "example.com",
		},
		{
			name:     "complex zone name",
			key:      "dns-backups/my-site.example.com-20240101-120000.json",
			expected: "my-site.example.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractZoneName(tt.key)
			if result != tt.expected {
				t.Errorf("extractZoneName(%q) = %q, want %q", tt.key, result, tt.expected)
			}
		})
	}
}

func TestNewBackupManager(t *testing.T) {
	config := &MinioConfig{
		Endpoint:  "localhost:9000",
		AccessKey: "test",
		SecretKey: "test",
		Bucket:    "test-bucket",
		UseSSL:    false,
	}

	manager := NewBackupManager(config)
	if manager == nil {
		t.Fatal("NewBackupManager returned nil")
	}

	if manager.minioConfig != config {
		t.Error("Minio config not set correctly")
	}

	if manager.verbosity != 1 {
		t.Errorf("Expected verbosity 1, got %d", manager.verbosity)
	}
}

func TestNewBackupManagerWithAWS(t *testing.T) {
	minioConfig := &MinioConfig{
		Endpoint:  "localhost:9000",
		AccessKey: "test",
		SecretKey: "test",
		Bucket:    "test-bucket",
		UseSSL:    false,
	}

	awsConfig := &AWSConfig{
		Vault:     "test-vault",
		AccountID: "123456789",
		AccessKey: "test",
		SecretKey: "test",
		Region:    "us-east-1",
	}

	manager := NewBackupManagerWithAWS(minioConfig, awsConfig)
	if manager == nil {
		t.Fatal("NewBackupManagerWithAWS returned nil")
	}

	if manager.minioConfig != minioConfig {
		t.Error("Minio config not set correctly")
	}

	if manager.awsConfig != awsConfig {
		t.Error("AWS config not set correctly")
	}

	if manager.verbosity != 1 {
		t.Errorf("Expected verbosity 1, got %d", manager.verbosity)
	}
}

func TestSetVerbosity(t *testing.T) {
	manager := NewBackupManager(&MinioConfig{})

	testCases := []int{0, 1, 2, 3, 4}
	for _, level := range testCases {
		manager.SetVerbosity(level)
		if manager.verbosity != level {
			t.Errorf("SetVerbosity(%d): got %d", level, manager.verbosity)
		}
	}
}

func TestDNSBackupInfo(t *testing.T) {
	info := DNSBackupInfo{
		Key:          "test-key",
		ZoneName:     "example.com",
		Size:         12345,
		LastModified: time.Now(),
	}

	if info.Key != "test-key" {
		t.Errorf("Expected key 'test-key', got %s", info.Key)
	}

	if info.ZoneName != "example.com" {
		t.Errorf("Expected zone 'example.com', got %s", info.ZoneName)
	}

	if info.Size != 12345 {
		t.Errorf("Expected size 12345, got %d", info.Size)
	}
}
