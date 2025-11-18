package health

import (
	"testing"
	"time"
)

func TestHTTPChecker(t *testing.T) {
	checker := &HTTPChecker{
		Timeout:         10 * time.Second,
		FollowRedirects: true,
		VerifySSL:       false,
	}

	// Test with a public URL
	result := checker.Check("https://www.google.com", nil)

	if result.Error != nil {
		t.Errorf("HTTP check failed: %v", result.Error)
	}

	if result.StatusCode != 200 {
		t.Errorf("Expected status 200, got %d", result.StatusCode)
	}

	if result.ResponseTime == 0 {
		t.Error("Response time should not be zero")
	}

	if result.DNSLookupTime == 0 {
		t.Error("DNS lookup time should not be zero")
	}
}

func TestPrompressMetricsParser(t *testing.T) {
	metrics := &PrompressMetrics{
		Available: true,
		RawMetrics: `# HELP wordpress_requests_total Total HTTP requests
# TYPE wordpress_requests_total counter
wordpress_requests_total 12345
# HELP wordpress_request_duration_seconds Average request duration
# TYPE wordpress_request_duration_seconds gauge
wordpress_request_duration_seconds 0.456
wordpress_errors_total 12
wordpress_php_memory_bytes 134217728
wordpress_db_queries_total 45678
wordpress_cache_hit_rate 0.87`,
	}

	metrics.parseMetrics()

	if metrics.MetricsCount != 6 {
		t.Errorf("Expected 6 metrics, got %d", metrics.MetricsCount)
	}

	if metrics.RequestRate != 12345 {
		t.Errorf("Expected request rate 12345, got %f", metrics.RequestRate)
	}

	if metrics.AvgResponseTime != 0.456 {
		t.Errorf("Expected avg response time 0.456, got %f", metrics.AvgResponseTime)
	}
}

func TestParseMemorySize(t *testing.T) {
	tests := []struct {
		input    string
		expected int64
	}{
		{"123B", 123},
		{"123KiB", 123 * 1024},
		{"123KB", 123 * 1024},
		{"123MiB", 123 * 1024 * 1024},
		{"1GiB", 1024 * 1024 * 1024},
	}

	for _, test := range tests {
		result := parseMemorySize(test.input)
		if result != test.expected {
			t.Errorf("parseMemorySize(%s) = %d, expected %d", test.input, result, test.expected)
		}
	}
}
