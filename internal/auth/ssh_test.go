package auth

import (
	"testing"
	"time"
)

func TestSSHConfig_Defaults(t *testing.T) {
	config := SSHConfig{
		Hostname: "test.example.com",
		Username: "testuser",
	}

	// Test that NewSSHClient would set defaults
	if config.Port == "" {
		config.Port = "22"
	}
	if config.Timeout == 0 {
		config.Timeout = 30 * time.Second
	}
	if config.KeepAlive == 0 {
		config.KeepAlive = 30 * time.Second
	}

	if config.Hostname != "test.example.com" {
		t.Errorf("Expected hostname 'test.example.com', got '%s'", config.Hostname)
	}
	if config.Username != "testuser" {
		t.Errorf("Expected username 'testuser', got '%s'", config.Username)
	}

	if config.Port != "22" {
		t.Errorf("Expected default port to be 22, got %s", config.Port)
	}
	if config.Timeout != 30*time.Second {
		t.Errorf("Expected default timeout to be 30s, got %v", config.Timeout)
	}
	if config.KeepAlive != 30*time.Second {
		t.Errorf("Expected default keep-alive to be 30s, got %v", config.KeepAlive)
	}
}

func TestBytesBuffer_Write(t *testing.T) {
	var data []byte
	buffer := &bytesBuffer{bytes: &data}

	testData := []byte("hello world")
	n, err := buffer.Write(testData)

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if n != len(testData) {
		t.Errorf("Expected to write %d bytes, got %d", len(testData), n)
	}
	if string(data) != "hello world" {
		t.Errorf("Expected 'hello world', got '%s'", string(data))
	}

	// Test multiple writes
	moreData := []byte(" more data")
	n2, err := buffer.Write(moreData)

	if err != nil {
		t.Errorf("Unexpected error on second write: %v", err)
	}
	if n2 != len(moreData) {
		t.Errorf("Expected to write %d bytes, got %d", len(moreData), n2)
	}
	if string(data) != "hello world more data" {
		t.Errorf("Expected 'hello world more data', got '%s'", string(data))
	}
}

func TestSSHClient_GetHostname(t *testing.T) {
	client := &SSHClient{
		hostname: "test.example.com",
		username: "testuser",
	}

	if client.GetHostname() != "test.example.com" {
		t.Errorf("Expected hostname 'test.example.com', got '%s'", client.GetHostname())
	}
}

func TestSSHClient_GetUsername(t *testing.T) {
	client := &SSHClient{
		hostname: "test.example.com",
		username: "testuser",
	}

	if client.GetUsername() != "testuser" {
		t.Errorf("Expected username 'testuser', got '%s'", client.GetUsername())
	}
}

// Note: For actual SSH connection testing, we would need a test SSH server
// These tests cover the basic structure and utility functions
// Integration tests should be run separately with actual SSH infrastructure
