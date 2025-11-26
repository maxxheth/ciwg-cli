package cmd

import (
	"testing"
)

func TestInferFormatFromFilename(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		fallback string
		expected string
	}{
		{
			name:     "JSON extension",
			filename: "inventory.json",
			fallback: "text",
			expected: "json",
		},
		{
			name:     "CSV extension",
			filename: "output.csv",
			fallback: "json",
			expected: "csv",
		},
		{
			name:     "JSON extension with path",
			filename: "/path/to/data.json",
			fallback: "text",
			expected: "json",
		},
		{
			name:     "CSV extension with path",
			filename: "/path/to/data.csv",
			fallback: "text",
			expected: "csv",
		},
		{
			name:     "No extension - use fallback",
			filename: "inventory",
			fallback: "json",
			expected: "json",
		},
		{
			name:     "Empty filename - use fallback",
			filename: "",
			fallback: "text",
			expected: "text",
		},
		{
			name:     "Unknown extension - use fallback",
			filename: "data.txt",
			fallback: "json",
			expected: "json",
		},
		{
			name:     "Mixed case extension",
			filename: "data.JSON",
			fallback: "text",
			expected: "json",
		},
		{
			name:     "Mixed case CSV",
			filename: "data.CSV",
			fallback: "text",
			expected: "csv",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := inferFormatFromFilename(tt.filename, tt.fallback)
			if result != tt.expected {
				t.Errorf("inferFormatFromFilename(%q, %q) = %q, expected %q",
					tt.filename, tt.fallback, result, tt.expected)
			}
		})
	}
}
