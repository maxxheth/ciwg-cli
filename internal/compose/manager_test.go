package compose

import (
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestComposeConfigMarshaling(t *testing.T) {
	config := &ComposeConfig{
		Version: "3.8",
		Services: map[string]Service{
			"web": {
				Image:         "nginx:latest",
				ContainerName: "web_container",
				Ports:         []interface{}{"80:80", "443:443"},
				Environment: map[string]string{
					"ENV": "production",
				},
				Restart: "unless-stopped",
			},
		},
	}

	// Test marshaling
	data, err := yaml.Marshal(config)
	if err != nil {
		t.Fatalf("Failed to marshal config: %v", err)
	}

	// Test unmarshaling
	var parsed ComposeConfig
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Failed to unmarshal config: %v", err)
	}

	// Verify data
	if parsed.Version != "3.8" {
		t.Errorf("Expected version 3.8, got %s", parsed.Version)
	}

	if len(parsed.Services) != 1 {
		t.Errorf("Expected 1 service, got %d", len(parsed.Services))
	}

	web, exists := parsed.Services["web"]
	if !exists {
		t.Fatal("Service 'web' not found")
	}

	if web.Image != "nginx:latest" {
		t.Errorf("Expected image nginx:latest, got %s", web.Image)
	}

	if web.ContainerName != "web_container" {
		t.Errorf("Expected container_name web_container, got %s", web.ContainerName)
	}
}

func TestBackupInfo(t *testing.T) {
	backup := BackupInfo{
		Path:      "/var/opt/site/docker-compose.yml.backup.20231027-120000",
		Timestamp: time.Now(),
		Container: "wp_testsite",
	}

	if backup.Path == "" {
		t.Error("Backup path should not be empty")
	}

	if backup.Container != "wp_testsite" {
		t.Errorf("Expected container wp_testsite, got %s", backup.Container)
	}
}

func TestServiceStructure(t *testing.T) {
	service := Service{
		Image:         "wordpress:latest",
		ContainerName: "wp_container",
		Environment: map[string]string{
			"WORDPRESS_DB_HOST": "db:3306",
			"WORDPRESS_DB_NAME": "wordpress",
		},
		Ports: []interface{}{
			"8080:80",
		},
		Volumes: []interface{}{
			"./html:/var/www/html",
		},
		Networks: []string{"backend"},
		Restart:  "always",
		Labels: map[string]string{
			"com.example.description": "WordPress container",
		},
	}

	if service.Image != "wordpress:latest" {
		t.Errorf("Expected image wordpress:latest, got %s", service.Image)
	}

	if service.Restart != "always" {
		t.Errorf("Expected restart always, got %s", service.Restart)
	}

	if len(service.Ports) != 1 {
		t.Errorf("Expected 1 port, got %d", len(service.Ports))
	}

	if len(service.Volumes) != 1 {
		t.Errorf("Expected 1 volume, got %d", len(service.Volumes))
	}
}

func TestComplexServiceEnvironment(t *testing.T) {
	// Test with map environment
	service1 := Service{
		Environment: map[string]string{
			"KEY1": "value1",
			"KEY2": "value2",
		},
	}

	// Test with array environment
	service2 := Service{
		Environment: []string{
			"KEY1=value1",
			"KEY2=value2",
		},
	}

	if service1.Environment == nil {
		t.Error("Map environment should not be nil")
	}

	if service2.Environment == nil {
		t.Error("Array environment should not be nil")
	}
}

func TestComposeConfigWithNetworks(t *testing.T) {
	config := &ComposeConfig{
		Version: "3.8",
		Services: map[string]Service{
			"app": {
				Image:    "myapp:latest",
				Networks: []string{"frontend", "backend"},
			},
		},
		Networks: map[string]interface{}{
			"frontend": map[string]interface{}{
				"driver": "bridge",
			},
			"backend": map[string]interface{}{
				"driver":   "bridge",
				"internal": true,
			},
		},
	}

	if len(config.Networks) != 2 {
		t.Errorf("Expected 2 networks, got %d", len(config.Networks))
	}

	if config.Services["app"].Image != "myapp:latest" {
		t.Error("Service image mismatch")
	}
}

func TestComposeConfigWithVolumes(t *testing.T) {
	config := &ComposeConfig{
		Version: "3.8",
		Services: map[string]Service{
			"db": {
				Image: "mysql:8",
				Volumes: []interface{}{
					"db_data:/var/lib/mysql",
				},
			},
		},
		Volumes: map[string]interface{}{
			"db_data": map[string]interface{}{
				"driver": "local",
			},
		},
	}

	if len(config.Volumes) != 1 {
		t.Errorf("Expected 1 volume, got %d", len(config.Volumes))
	}

	dbService := config.Services["db"]
	if len(dbService.Volumes) != 1 {
		t.Errorf("Expected 1 service volume, got %d", len(dbService.Volumes))
	}
}
