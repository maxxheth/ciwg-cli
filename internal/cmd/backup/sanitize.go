package backup

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"ciwg-cli/internal/backup"
)

func runBackupSanitize(cmd *cobra.Command, args []string) error {
	inputPath := mustGetStringFlag(cmd, "input")
	outputPath := mustGetStringFlag(cmd, "output")
	extractDirStr := mustGetStringFlag(cmd, "extract-dir")
	extractFileStr := mustGetStringFlag(cmd, "extract-file")
	dryRun := mustGetBoolFlag(cmd, "dry-run")

	// Parse comma-separated lists
	var extractDirs []string
	for _, dir := range strings.Split(extractDirStr, ",") {
		if trimmed := strings.TrimSpace(dir); trimmed != "" {
			extractDirs = append(extractDirs, trimmed)
		}
	}

	var extractFiles []string
	for _, file := range strings.Split(extractFileStr, ",") {
		if trimmed := strings.TrimSpace(file); trimmed != "" {
			extractFiles = append(extractFiles, trimmed)
		}
	}

	// Validate input
	if inputPath == "" {
		return fmt.Errorf("--input is required")
	}
	if outputPath == "" {
		return fmt.Errorf("--output is required")
	}

	// Check if input file exists
	if _, err := os.Stat(inputPath); os.IsNotExist(err) {
		return fmt.Errorf("input file does not exist: %s", inputPath)
	}

	fmt.Println("===========================================")
	fmt.Println("Backup Sanitization")
	fmt.Println("===========================================")
	if dryRun {
		fmt.Println("Mode: 🔍 DRY RUN (preview only)")
	} else {
		fmt.Println("Mode: 🚀 LIVE")
	}
	fmt.Printf("Input:         %s\n", inputPath)
	fmt.Printf("Output:        %s\n", outputPath)
	fmt.Printf("Extract Dirs:  %v\n", extractDirs)
	fmt.Printf("Extract Files: %v\n", extractFiles)
	fmt.Println("===========================================")

	// Create a backup manager (no SSH or Minio needed for sanitization)
	bm := backup.NewBackupManager(nil, nil)

	options := &backup.SanitizeOptions{
		InputPath:    inputPath,
		OutputPath:   outputPath,
		ExtractDirs:  extractDirs,
		ExtractFiles: extractFiles,
		DryRun:       dryRun,
	}

	if err := bm.SanitizeBackup(options); err != nil {
		return fmt.Errorf("sanitization failed: %w", err)
	}

	if dryRun {
		fmt.Println("\n✓ Dry run complete. No changes were made.")
	} else {
		fmt.Printf("\n✓ Sanitization complete! Output: %s\n", outputPath)
	}

	return nil
}
