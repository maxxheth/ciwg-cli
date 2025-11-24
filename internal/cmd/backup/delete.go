package backup

import (
	"fmt"
	"strings"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"

	"ciwg-cli/internal/backup"
)

func runBackupDelete(cmd *cobra.Command, args []string) error {
	if envPath := mustGetStringFlag(cmd, "env"); envPath != "" {
		if err := godotenv.Load(envPath); err != nil {
			return fmt.Errorf("failed to load env file '%s': %w", envPath, err)
		}
	}

	// Validate Minio configuration
	minioConfig, err := getMinioConfig(cmd)
	if err != nil {
		return err
	}

	bm := backup.NewBackupManager(nil, minioConfig)

	var objectName string
	if len(args) > 0 {
		objectName = args[0]
	}

	prefix := mustGetStringFlag(cmd, "prefix")
	latest := mustGetBoolFlag(cmd, "latest")
	deleteAll := mustGetBoolFlag(cmd, "delete-all")
	deleteRange := mustGetStringFlag(cmd, "delete-range")
	deleteRangeByDate := mustGetStringFlag(cmd, "delete-range-by-date")
	skipConfirm := mustGetBoolFlag(cmd, "skip-confirmation")
	dryRun := mustGetBoolFlag(cmd, "dry-run")

	// Validate mutually exclusive flags
	flagCount := 0
	if objectName != "" {
		flagCount++
	}
	if latest {
		flagCount++
	}
	if deleteAll {
		flagCount++
	}
	if deleteRange != "" {
		flagCount++
	}
	if deleteRangeByDate != "" {
		flagCount++
	}
	if flagCount > 1 {
		return fmt.Errorf("only one of: object argument, --latest, --delete-all, --delete-range, or --delete-range-by-date can be specified")
	}

	// Resolve object(s) to delete
	var toDelete []string
	if objectName != "" {
		toDelete = append(toDelete, objectName)
	} else if prefix != "" || deleteAll || deleteRange != "" || deleteRangeByDate != "" {
		limit := 0 // Get all objects for these operations
		if v, err := cmd.Flags().GetInt("limit"); err == nil && !deleteAll && deleteRange == "" && deleteRangeByDate == "" {
			limit = v
		}
		objs, err := bm.ListBackups(prefix, limit)
		if err != nil {
			return fmt.Errorf("failed to list objects for prefix '%s': %w", prefix, err)
		}
		if len(objs) == 0 {
			fmt.Println("No objects found for prefix")
			return nil
		}

		if latest {
			// pick latest
			latestKey := objs[0].Key
			latestTime := objs[0].LastModified
			for _, o := range objs[1:] {
				if o.LastModified.After(latestTime) {
					latestKey = o.Key
					latestTime = o.LastModified
				}
			}
			toDelete = append(toDelete, latestKey)
		} else if deleteAll {
			for _, o := range objs {
				toDelete = append(toDelete, o.Key)
			}
		} else if deleteRange != "" {
			// Parse numeric range and select objects
			start, end, err := bm.ParseNumericRange(deleteRange)
			if err != nil {
				return fmt.Errorf("invalid --delete-range format: %w", err)
			}
			selected, err := bm.SelectObjectsByNumericRange(objs, start, end)
			if err != nil {
				return fmt.Errorf("failed to select objects by range: %w", err)
			}
			for _, o := range selected {
				toDelete = append(toDelete, o.Key)
			}
		} else if deleteRangeByDate != "" {
			// Parse date range and filter objects
			startTime, endTime, err := bm.ParseDateRange(deleteRangeByDate)
			if err != nil {
				return fmt.Errorf("invalid --delete-range-by-date format: %w", err)
			}
			filtered := bm.FilterObjectsByDateRange(objs, startTime, endTime)
			for _, o := range filtered {
				toDelete = append(toDelete, o.Key)
			}
		} else {
			for _, o := range objs {
				toDelete = append(toDelete, o.Key)
			}
		}
	} else {
		return fmt.Errorf("object name argument or --prefix is required")
	}

	// Confirmation
	// If dry-run requested, just preview and exit
	if dryRun {
		fmt.Printf("Dry run: %d object(s) would be deleted:\n", len(toDelete))
		for _, k := range toDelete {
			fmt.Println(" - ", k)
		}
		return nil
	}

	if !skipConfirm {
		fmt.Printf("About to delete %d object(s). Continue? [y/N]: ", len(toDelete))
		var resp string
		if _, err := fmt.Scanln(&resp); err != nil {
			return fmt.Errorf("confirmation failed: %w", err)
		}
		resp = strings.TrimSpace(strings.ToLower(resp))
		if resp != "y" && resp != "yes" {
			fmt.Println("Aborted by user")
			return nil
		}
	}

	// Perform deletion
	if err := bm.DeleteObjects(toDelete); err != nil {
		return fmt.Errorf("failed to delete objects: %w", err)
	}

	fmt.Printf("Deleted %d object(s)\n", len(toDelete))
	return nil
}
