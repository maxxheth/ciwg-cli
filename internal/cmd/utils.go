package cmd

import (
	"fmt"
	"strconv"
	"strings"
)

var (
	outputFile  string
	serverRange string
)

func parseServerRange(pattern string) (string, int, int, error) {
	// Expected format: "wp%d.ciwgserver.com:0-41"
	parts := strings.Split(pattern, ":")
	if len(parts) != 2 {
		return "", 0, 0, fmt.Errorf("invalid server range format, expected 'pattern:start-end'")
	}

	rangeParts := strings.Split(parts[1], "-")
	if len(rangeParts) != 2 {
		return "", 0, 0, fmt.Errorf("invalid range format, expected 'start-end'")
	}

	start, err := strconv.Atoi(rangeParts[0])
	if err != nil {
		return "", 0, 0, fmt.Errorf("invalid start number: %w", err)
	}

	end, err := strconv.Atoi(rangeParts[1])
	if err != nil {
		return "", 0, 0, fmt.Errorf("invalid end number: %w", err)
	}

	return parts[0], start, end, nil
}
