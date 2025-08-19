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

func parseServerRange(pattern string) (string, int, int, map[int]bool, error) {
	// Expected format: "wp%d.ciwgserver.com:0-41"
	// Or with exclusions: "wp%d.ciwgserver.com:0-41:!10,15-17,22"
	parts := strings.Split(pattern, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return "", 0, 0, nil, fmt.Errorf("invalid server range format, expected 'pattern:start-end' or 'pattern:start-end:!exclusions'")
	}

	// Parse main range
	rangeParts := strings.Split(parts[1], "-")
	if len(rangeParts) != 2 {
		return "", 0, 0, nil, fmt.Errorf("invalid range format, expected 'start-end'")
	}

	start, err := strconv.Atoi(rangeParts[0])
	if err != nil {
		return "", 0, 0, nil, fmt.Errorf("invalid start number: %w", err)
	}

	end, err := strconv.Atoi(rangeParts[1])
	if err != nil {
		return "", 0, 0, nil, fmt.Errorf("invalid end number: %w", err)
	}

	if start > end {
		return "", 0, 0, nil, fmt.Errorf("start of range cannot be greater than end")
	}

	// Parse exclusions
	exclusions := make(map[int]bool)
	if len(parts) == 3 {
		exclusionStr := parts[2]
		if !strings.HasPrefix(exclusionStr, "!") {
			return "", 0, 0, nil, fmt.Errorf("exclusions part must start with '!'")
		}
		exclusionStr = strings.TrimPrefix(exclusionStr, "!")

		for _, exPart := range strings.Split(exclusionStr, ",") {
			exPart = strings.TrimSpace(exPart)
			if exPart == "" {
				continue
			}

			if strings.Contains(exPart, "-") { // It's a range
				subRangeParts := strings.Split(exPart, "-")
				if len(subRangeParts) != 2 {
					return "", 0, 0, nil, fmt.Errorf("invalid exclusion range format: %s", exPart)
				}
				exStart, err := strconv.Atoi(subRangeParts[0])
				if err != nil {
					return "", 0, 0, nil, fmt.Errorf("invalid exclusion start number in '%s': %w", exPart, err)
				}
				exEnd, err := strconv.Atoi(subRangeParts[1])
				if err != nil {
					return "", 0, 0, nil, fmt.Errorf("invalid exclusion end number in '%s': %w", exPart, err)
				}
				if exStart > exEnd {
					return "", 0, 0, nil, fmt.Errorf("invalid exclusion range: start > end in %s", exPart)
				}
				for i := exStart; i <= exEnd; i++ {
					exclusions[i] = true
				}
			} else { // It's a single number
				exNum, err := strconv.Atoi(exPart)
				if err != nil {
					return "", 0, 0, nil, fmt.Errorf("invalid exclusion number '%s': %w", exPart, err)
				}
				exclusions[exNum] = true
			}
		}
	}

	return parts[0], start, end, exclusions, nil
}
