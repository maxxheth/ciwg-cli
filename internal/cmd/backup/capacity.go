package backup

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"

	"ciwg-cli/internal/backup"
)

func runBackupEstimateCapacity(cmd *cobra.Command, args []string) error {
	// Load .env if specified
	if envPath := mustGetStringFlag(cmd, "env"); envPath != "" {
		if err := godotenv.Load(envPath); err != nil {
			return fmt.Errorf("failed to load env file '%s': %w", envPath, err)
		}
	}

	// Parse flags
	serverRange := mustGetStringFlag(cmd, "server-range")
	estimateMethod := mustGetStringFlag(cmd, "estimate-method")
	sampleSize, _ := cmd.Flags().GetInt64("sample-size")
	fromBackup := mustGetStringFlag(cmd, "from-backup")
	avgSizeStr := mustGetStringFlag(cmd, "avg-compressed-size")
	siteCount, _ := cmd.Flags().GetInt("site-count")
	dailyRetention, _ := cmd.Flags().GetInt("daily-retention")
	weeklyRetention, _ := cmd.Flags().GetInt("weekly-retention")
	monthlyRetention, _ := cmd.Flags().GetInt("monthly-retention")
	estimateFocus := mustGetStringFlag(cmd, "estimate-focus")
	estimateType := mustGetStringFlag(cmd, "estimate-type")
	outputFormat := mustGetStringFlag(cmd, "output")
	growthRate, _ := cmd.Flags().GetFloat64("growth-rate")
	projectionMonths, _ := cmd.Flags().GetInt("projection-months")
	bufferPercent, _ := cmd.Flags().GetFloat64("buffer-percent")
	glacierPrice, _ := cmd.Flags().GetFloat64("aws-glacier-price")
	retrievalPrice, _ := cmd.Flags().GetFloat64("aws-retrieval-price")
	parentDir := mustGetStringFlag(cmd, "container-parent-dir")
	availableStorageStr := mustGetStringFlag(cmd, "available-storage")

	// Parse available storage if provided
	var availableStorageGB float64
	if availableStorageStr != "" {
		sizeBytes, err := parseSize(availableStorageStr)
		if err != nil {
			return fmt.Errorf("invalid --available-storage: %w", err)
		}
		availableStorageGB = float64(sizeBytes) / (1024 * 1024 * 1024)
	}

	// Validate estimate-focus
	if estimateFocus != "growth-modeling" && estimateFocus != "static-capacity" && estimateFocus != "all" {
		return fmt.Errorf("invalid --estimate-focus: %s (must be 'growth-modeling', 'static-capacity', or 'all')", estimateFocus)
	}

	// Validate estimate-type
	if estimateType != "cost" && estimateType != "size" && estimateType != "all" {
		return fmt.Errorf("invalid --estimate-type: %s (must be 'cost', 'size', or 'all')", estimateType)
	}

	// Validate output format
	if outputFormat != "stdout" && outputFormat != "json" && outputFormat != "csv" {
		return fmt.Errorf("invalid --output: %s (must be 'stdout', 'json', or 'csv')", outputFormat)
	}

	// Determine data source
	var hostname string
	if len(args) > 0 {
		hostname = args[0]
	}

	// Validate input methods
	inputCount := 0
	if hostname != "" || serverRange != "" {
		inputCount++
	}
	if fromBackup != "" {
		inputCount++
	}
	if avgSizeStr != "" {
		inputCount++
	}

	if inputCount == 0 {
		return fmt.Errorf("must specify one data source: hostname/--server-range, --from-backup, or --avg-compressed-size")
	}
	if inputCount > 1 {
		return fmt.Errorf("only one data source can be specified at a time")
	}

	// Validate manual input
	if avgSizeStr != "" && siteCount == 0 {
		return fmt.Errorf("--site-count is required when using --avg-compressed-size")
	}

	// Create capacity options
	capacityOpts := &backup.CapacityEstimateOptions{
		DailyRetention:      dailyRetention,
		WeeklyRetention:     weeklyRetention,
		MonthlyRetention:    monthlyRetention,
		GrowthRate:          growthRate,
		ProjectionMonths:    projectionMonths,
		BufferPercent:       bufferPercent,
		GlacierPricePerGB:   glacierPrice,
		RetrievalPricePerGB: retrievalPrice,
	}

	var estimate *backup.CapacityEstimate
	var err error

	// Process based on data source
	if avgSizeStr != "" {
		// Manual input mode
		avgSize, parseErr := parseSize(avgSizeStr)
		if parseErr != nil {
			return fmt.Errorf("invalid --avg-compressed-size format: %w", parseErr)
		}

		// Create a minimal backup manager (no SSH/Minio needed for manual calc)
		manager := backup.NewBackupManager(nil, nil)
		estimate, err = manager.EstimateCapacityFromManual(avgSize, siteCount, capacityOpts)
		if err != nil {
			return fmt.Errorf("capacity estimation failed: %w", err)
		}

	} else if fromBackup != "" {
		// From existing backup mode
		minioConfig, cfgErr := getMinioConfig(cmd)
		if cfgErr != nil {
			return fmt.Errorf("Minio configuration required for --from-backup: %w", cfgErr)
		}

		manager := backup.NewBackupManager(nil, minioConfig)
		estimate, err = manager.EstimateCapacityFromBackup(fromBackup, siteCount, capacityOpts)
		if err != nil {
			return fmt.Errorf("capacity estimation from backup failed: %w", err)
		}

	} else {
		// Live scanning mode (hostname or server-range)
		if hostname != "" {
			// Single server scan
			sshClient, sshErr := createSSHClient(cmd, hostname)
			if sshErr != nil {
				return fmt.Errorf("failed to connect to %s: %w", hostname, sshErr)
			}
			defer sshClient.Close()

			manager := backup.NewBackupManager(sshClient, nil)

			// Get containers
			containers, containerErr := manager.GetContainersFromOptions(&backup.BackupOptions{
				ParentDir: parentDir,
			})
			if containerErr != nil {
				return fmt.Errorf("failed to get containers: %w", containerErr)
			}

			estimate, err = manager.EstimateCapacityFromScan(containers, estimateMethod, sampleSize, capacityOpts)
			if err != nil {
				return fmt.Errorf("capacity estimation failed: %w", err)
			}

		} else {
			// Server range scan
			estimate, err = processCapacityEstimateForServerRange(cmd, serverRange, estimateMethod, sampleSize, parentDir, capacityOpts, outputFormat)
			if err != nil {
				return err
			}
		}
	}

	// Output results based on format
	switch outputFormat {
	case "json":
		return outputCapacityJSON(estimate)
	case "csv":
		return outputCapacityCSV(estimate, estimateFocus, estimateType)
	default:
		if err := outputCapacityStdout(estimate, estimateFocus, estimateType); err != nil {
			return err
		}
		// Output recommendations if available storage was specified
		outputCapacityRecommendations(estimate, availableStorageGB)
		return nil
	}
}

// processCapacityEstimateForServerRange handles server range processing
func processCapacityEstimateForServerRange(cmd *cobra.Command, serverRange, estimateMethod string, sampleSize int64, parentDir string, options *backup.CapacityEstimateOptions, outputFormat string) (*backup.CapacityEstimate, error) {
	pattern, start, end, exclusions, err := parseServerRange(serverRange)
	if err != nil {
		return nil, err
	}

	// Collect estimates from each server
	var serverEstimates []*backup.CapacityEstimate
	var allSites []backup.SiteEstimate
	totalServers := 0
	successfulServers := 0
	totalContainers := 0

	// Suppress progress output for JSON/CSV formats
	quiet := outputFormat == "json" || outputFormat == "csv"

	if !quiet {
		fmt.Printf("🌐 Scanning server range: %s\n\n", serverRange)
	}

	for i := start; i <= end; i++ {
		if exclusions[i] {
			continue
		}
		totalServers++

		hostname := fmt.Sprintf(pattern, i)
		if !quiet {
			fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
			fmt.Printf("Server: %s\n", hostname)
			fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n\n")
		}

		sshClient, err := createSSHClient(cmd, hostname)
		if err != nil {
			if !quiet {
				fmt.Printf("⚠️  Failed to connect to %s: %v\n\n", hostname, err)
			}
			continue
		}

		manager := backup.NewBackupManager(sshClient, nil)
		containers, err := manager.GetContainersFromOptions(&backup.BackupOptions{
			ParentDir: parentDir,
		})

		if err != nil {
			if !quiet {
				fmt.Printf("⚠️  Failed to get containers from %s: %v\n\n", hostname, err)
			}
			sshClient.Close()
			continue
		}

		if len(containers) == 0 {
			if !quiet {
				fmt.Printf("ℹ️  No containers found on %s\n\n", hostname)
			}
			sshClient.Close()
			continue
		}

		if !quiet {
			fmt.Printf("Found %d container(s) on %s\n\n", len(containers), hostname)
		}

		// Scan this server's containers
		estimate, err := manager.EstimateCapacityFromScan(containers, estimateMethod, sampleSize, options)
		sshClient.Close()

		if err != nil {
			if !quiet {
				fmt.Printf("⚠️  Failed to estimate capacity for %s: %v\n\n", hostname, err)
			}
			continue
		}

		serverEstimates = append(serverEstimates, estimate)
		allSites = append(allSites, estimate.Sites...)
		totalContainers += len(containers)
		successfulServers++

		// Show server summary
		if !quiet {
			fmt.Printf("Server %s Summary:\n", hostname)
			fmt.Printf("  Sites: %d, Avg compressed: %.2f MB\n",
				len(estimate.Sites),
				float64(estimate.AvgCompressedSize)/(1024*1024))
			fmt.Printf("  Server total: %.2f GB compressed\n\n",
				float64(estimate.AvgCompressedSize*int64(len(estimate.Sites)))/(1024*1024*1024))
		}
	}

	if successfulServers == 0 {
		return nil, fmt.Errorf("failed to scan any servers (tried %d)", totalServers)
	}

	if !quiet {
		fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		fmt.Printf("📊 FLEET-WIDE AGGREGATION\n")
		fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n\n")
		fmt.Printf("Successfully scanned: %d/%d servers, %d total containers\n\n", successfulServers, totalServers, totalContainers)
	}

	// Aggregate all server estimates into one combined result
	combinedEstimate := &backup.CapacityEstimate{
		EstimationMethod:    estimateMethod,
		SitesScanned:        len(allSites),
		DailyRetention:      options.DailyRetention,
		WeeklyRetention:     options.WeeklyRetention,
		MonthlyRetention:    options.MonthlyRetention,
		TotalBackupsPerSite: options.DailyRetention + options.WeeklyRetention + options.MonthlyRetention,
		BufferPercent:       options.BufferPercent,
		Sites:               allSites,
	}

	// Calculate combined totals
	var totalCompressed int64
	var totalUncompressed int64
	for _, site := range allSites {
		totalCompressed += site.CompressedSize
		totalUncompressed += site.UncompressedSize
	}

	if len(allSites) > 0 {
		combinedEstimate.AvgCompressedSize = totalCompressed / int64(len(allSites))
		combinedEstimate.AvgUncompressedSize = totalUncompressed / int64(len(allSites))
		if combinedEstimate.AvgUncompressedSize > 0 {
			combinedEstimate.AvgCompressionRatio = (1.0 - float64(combinedEstimate.AvgCompressedSize)/float64(combinedEstimate.AvgUncompressedSize)) * 100
		}
	}

	// Calculate per-site storage requirements
	combinedEstimate.PerSiteHotStorage = combinedEstimate.AvgCompressedSize * int64(options.DailyRetention)
	combinedEstimate.PerSiteColdStorage = combinedEstimate.AvgCompressedSize * int64(options.WeeklyRetention+options.MonthlyRetention)
	combinedEstimate.PerSiteTotalStorage = combinedEstimate.PerSiteHotStorage + combinedEstimate.PerSiteColdStorage

	// Calculate fleet-wide storage
	combinedEstimate.FleetHotStorage = combinedEstimate.PerSiteHotStorage * int64(len(allSites))
	combinedEstimate.FleetColdStorage = combinedEstimate.PerSiteColdStorage * int64(len(allSites))
	combinedEstimate.FleetTotalStorage = combinedEstimate.FleetHotStorage + combinedEstimate.FleetColdStorage

	// Add buffer
	combinedEstimate.FleetTotalWithBuffer = int64(float64(combinedEstimate.FleetTotalStorage) * (1.0 + options.BufferPercent/100.0))

	// Calculate growth projections if growth rate specified
	if options.GrowthRate > 0 && options.ProjectionMonths > 0 {
		combinedEstimate.GrowthProjections = calculateGrowthProjections(
			combinedEstimate.FleetHotStorage,
			combinedEstimate.FleetColdStorage,
			options.GrowthRate,
			options.ProjectionMonths,
			options.GlacierPricePerGB,
		)
	}

	// Calculate costs if price specified
	if options.GlacierPricePerGB > 0 {
		coldStorageGB := float64(combinedEstimate.FleetColdStorage) / (1024 * 1024 * 1024)
		combinedEstimate.MonthlyCost = coldStorageGB * options.GlacierPricePerGB

		if options.RetrievalPricePerGB > 0 {
			combinedEstimate.RetrievalCost10Pct = coldStorageGB * 0.10 * options.RetrievalPricePerGB
		}
	}

	return combinedEstimate, nil
}

// calculateGrowthProjections computes storage growth projections
func calculateGrowthProjections(hotStorage, coldStorage int64, growthRate float64, months int, glacierPrice float64) []backup.GrowthProjection {
	projections := make([]backup.GrowthProjection, 0, months)

	currentTotal := float64(hotStorage + coldStorage)
	growthMultiplier := 1.0 + (growthRate / 100.0)

	for month := 1; month <= months; month++ {
		currentTotal *= growthMultiplier
		totalGB := currentTotal / (1024 * 1024 * 1024)

		// Assume same hot/cold ratio
		ratio := float64(coldStorage) / float64(hotStorage+coldStorage)
		coldGB := totalGB * ratio
		hotGB := totalGB * (1.0 - ratio)

		projection := backup.GrowthProjection{
			Month:          month,
			TotalStorageGB: totalGB,
			HotStorageGB:   hotGB,
			ColdStorageGB:  coldGB,
		}

		if glacierPrice > 0 {
			projection.MonthlyCost = coldGB * glacierPrice
		}

		projections = append(projections, projection)
	}

	return projections
}

// outputCapacityJSON outputs estimate as JSON
func outputCapacityJSON(estimate *backup.CapacityEstimate) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(estimate)
}

// outputCapacityCSV outputs estimate as CSV
func outputCapacityCSV(estimate *backup.CapacityEstimate, focus, estimateType string) error {
	writer := csv.NewWriter(os.Stdout)
	defer writer.Flush()

	// Write header
	if err := writer.Write([]string{"Metric", "Value", "Unit"}); err != nil {
		return err
	}

	// Basic info
	writeCSVRow := func(metric, value, unit string) error {
		return writer.Write([]string{metric, value, unit})
	}

	writeCSVRow("Estimation Method", estimate.EstimationMethod, "")
	writeCSVRow("Sites Scanned", fmt.Sprintf("%d", estimate.SitesScanned), "sites")
	writeCSVRow("Daily Retention", fmt.Sprintf("%d", estimate.DailyRetention), "days")
	writeCSVRow("Weekly Retention", fmt.Sprintf("%d", estimate.WeeklyRetention), "weeks")
	writeCSVRow("Monthly Retention", fmt.Sprintf("%d", estimate.MonthlyRetention), "months")
	writeCSVRow("Total Backups Per Site", fmt.Sprintf("%d", estimate.TotalBackupsPerSite), "backups")

	if focus == "static-capacity" || focus == "all" {
		if estimateType == "size" || estimateType == "all" {
			// Storage sizes
			writeCSVRow("Avg Compressed Size", fmt.Sprintf("%.2f", float64(estimate.AvgCompressedSize)/(1024*1024)), "MB")
			writeCSVRow("Per-Site Hot Storage", fmt.Sprintf("%.2f", float64(estimate.PerSiteHotStorage)/(1024*1024)), "MB")
			writeCSVRow("Per-Site Cold Storage", fmt.Sprintf("%.2f", float64(estimate.PerSiteColdStorage)/(1024*1024)), "MB")
			writeCSVRow("Per-Site Total", fmt.Sprintf("%.2f", float64(estimate.PerSiteTotalStorage)/(1024*1024)), "MB")
			writeCSVRow("Fleet Hot Storage", fmt.Sprintf("%.2f", float64(estimate.FleetHotStorage)/(1024*1024*1024)), "GB")
			writeCSVRow("Fleet Cold Storage", fmt.Sprintf("%.2f", float64(estimate.FleetColdStorage)/(1024*1024*1024)), "GB")
			writeCSVRow("Fleet Total", fmt.Sprintf("%.2f", float64(estimate.FleetTotalStorage)/(1024*1024*1024)), "GB")
			writeCSVRow("Fleet Total With Buffer", fmt.Sprintf("%.2f", float64(estimate.FleetTotalWithBuffer)/(1024*1024*1024)), "GB")
		}

		if estimateType == "cost" || estimateType == "all" {
			writeCSVRow("Monthly Storage Cost", fmt.Sprintf("%.2f", estimate.MonthlyCost), "USD")
			writeCSVRow("Retrieval Cost (10%)", fmt.Sprintf("%.2f", estimate.RetrievalCost10Pct), "USD")
		}
	}

	// Growth projections
	if (focus == "growth-modeling" || focus == "all") && len(estimate.GrowthProjections) > 0 {
		writer.Write([]string{}) // Blank line
		writer.Write([]string{"Growth Projections", "", ""})
		writer.Write([]string{"Month", "Total Storage (GB)", "Monthly Cost (USD)"})

		for _, proj := range estimate.GrowthProjections {
			costStr := ""
			if proj.MonthlyCost > 0 {
				costStr = fmt.Sprintf("%.2f", proj.MonthlyCost)
			}
			writer.Write([]string{
				fmt.Sprintf("%d", proj.Month),
				fmt.Sprintf("%.2f", proj.TotalStorageGB),
				costStr,
			})
		}
	}

	return nil
}

// outputCapacityStdout outputs estimate to terminal
func outputCapacityStdout(estimate *backup.CapacityEstimate, focus, estimateType string) error {
	fmt.Println("===========================================")
	fmt.Println("Backup Capacity Estimation")
	fmt.Println("===========================================")
	fmt.Printf("Estimation Method:  %s\n", estimate.EstimationMethod)
	fmt.Printf("Sites Analyzed:     %d\n", estimate.SitesScanned)
	fmt.Println()

	fmt.Println("Retention Policy:")
	fmt.Printf("  Daily backups:    %d days\n", estimate.DailyRetention)
	fmt.Printf("  Weekly backups:   %d weeks\n", estimate.WeeklyRetention)
	fmt.Printf("  Monthly backups:  %d months\n", estimate.MonthlyRetention)
	fmt.Printf("  Total per site:   %d backups\n", estimate.TotalBackupsPerSite)
	fmt.Println()

	if focus == "static-capacity" || focus == "all" {
		if estimateType == "size" || estimateType == "all" {
			// Calculate total baseline (single backup of all sites)
			totalBaselineUncompressed := estimate.AvgUncompressedSize * int64(estimate.SitesScanned)
			totalBaselineCompressed := estimate.AvgCompressedSize * int64(estimate.SitesScanned)

			fmt.Println("Baseline Measurements (per site average):")
			if estimate.AvgUncompressedSize > 0 {
				fmt.Printf("  Avg uncompressed:  %.2f MB\n", float64(estimate.AvgUncompressedSize)/(1024*1024))
			}
			fmt.Printf("  Avg compressed:    %.2f MB\n", float64(estimate.AvgCompressedSize)/(1024*1024))
			if estimate.AvgCompressionRatio > 0 {
				fmt.Printf("  Compression ratio: %.1f%% saved\n", estimate.AvgCompressionRatio)
			}
			fmt.Println()

			fmt.Printf("Total Baseline Size (1 backup of all %d sites):\n", estimate.SitesScanned)
			if totalBaselineUncompressed > 0 {
				fmt.Printf("  Total uncompressed: %.2f GB\n", float64(totalBaselineUncompressed)/(1024*1024*1024))
			}
			fmt.Printf("  Total compressed:   %.2f GB\n", float64(totalBaselineCompressed)/(1024*1024*1024))
			fmt.Println()

			fmt.Println("Per-Site Storage Requirements:")
			fmt.Printf("  Hot storage (Minio):  %.2f GB (%d daily backups)\n",
				float64(estimate.PerSiteHotStorage)/(1024*1024*1024),
				estimate.DailyRetention)
			fmt.Printf("  Cold storage (AWS):   %.2f GB (%d weekly + %d monthly)\n",
				float64(estimate.PerSiteColdStorage)/(1024*1024*1024),
				estimate.WeeklyRetention,
				estimate.MonthlyRetention)
			fmt.Printf("  Total per site:       %.2f GB\n",
				float64(estimate.PerSiteTotalStorage)/(1024*1024*1024))
			fmt.Println()

			fmt.Printf("Fleet-Wide Storage (%d sites):\n", estimate.SitesScanned)
			fmt.Printf("  Hot storage (Minio):  %.2f GB\n", float64(estimate.FleetHotStorage)/(1024*1024*1024))
			fmt.Printf("  Cold storage (AWS):   %.2f GB\n", float64(estimate.FleetColdStorage)/(1024*1024*1024))
			fmt.Printf("  Total required:       %.2f GB\n", float64(estimate.FleetTotalStorage)/(1024*1024*1024))
			fmt.Printf("  With %.0f%% buffer:      %.2f GB\n",
				estimate.BufferPercent,
				float64(estimate.FleetTotalWithBuffer)/(1024*1024*1024))
			fmt.Println()
		}

		if estimateType == "cost" || estimateType == "all" {
			if estimate.MonthlyCost > 0 {
				fmt.Println("Cost Estimates (AWS Glacier):")
				fmt.Printf("  Monthly storage:      $%.2f\n", estimate.MonthlyCost)
				if estimate.RetrievalCost10Pct > 0 {
					fmt.Printf("  Retrieval (10%%/mo):  $%.2f\n", estimate.RetrievalCost10Pct)
				}
				fmt.Println()
			}
		}
	}

	if (focus == "growth-modeling" || focus == "all") && len(estimate.GrowthProjections) > 0 {
		fmt.Println("Growth Projections:")
		fmt.Println("  Month | Total Storage | Hot Storage | Cold Storage | Monthly Cost")
		fmt.Println("  ------|---------------|-------------|--------------|-------------")

		for _, proj := range estimate.GrowthProjections {
			costStr := "N/A"
			if proj.MonthlyCost > 0 {
				costStr = fmt.Sprintf("$%.2f", proj.MonthlyCost)
			}
			fmt.Printf("  %5d | %10.2f GB | %8.2f GB | %9.2f GB | %s\n",
				proj.Month,
				proj.TotalStorageGB,
				proj.HotStorageGB,
				proj.ColdStorageGB,
				costStr)
		}
		fmt.Println()
	}

	// Per-site breakdown if available and not too many
	if len(estimate.Sites) > 0 && len(estimate.Sites) <= 10 {
		fmt.Println("Per-Site Breakdown:")
		fmt.Println("  Site | Compressed | Hot Storage | Cold Storage | Total")
		fmt.Println("  -----|------------|-------------|--------------|-------")

		for _, site := range estimate.Sites {
			fmt.Printf("  %-25s | %7.2f MB | %8.2f MB | %9.2f MB | %.2f MB\n",
				site.SiteName,
				float64(site.CompressedSize)/(1024*1024),
				float64(site.HotStorageSize)/(1024*1024),
				float64(site.ColdStorageSize)/(1024*1024),
				float64(site.TotalStorageSize)/(1024*1024))
		}
		fmt.Println()
	} else if len(estimate.Sites) > 10 {
		fmt.Printf("ℹ️  %d sites analyzed (use --output json for full per-site breakdown)\n\n", len(estimate.Sites))
	}

	return nil
}

func outputCapacityRecommendations(estimate *backup.CapacityEstimate, availableStorageGB float64) {
	if availableStorageGB <= 0 {
		return
	}

	requiredHotStorageGB := float64(estimate.FleetHotStorage) / (1024 * 1024 * 1024)

	fmt.Println("===========================================")
	fmt.Println("Storage Analysis & Recommendations")
	fmt.Println("===========================================")
	fmt.Printf("Available Minio Storage: %.2f GB\n", availableStorageGB)
	fmt.Printf("Required Hot Storage:    %.2f GB (%d daily backups)\n",
		requiredHotStorageGB, estimate.DailyRetention)
	fmt.Println()

	// Calculate shortfall
	shortfall := requiredHotStorageGB - availableStorageGB
	utilizationPct := (requiredHotStorageGB / availableStorageGB) * 100

	if shortfall > 0 {
		// INSUFFICIENT CAPACITY
		fmt.Printf("⚠️  CRITICAL: Storage shortfall of %.2f GB (%.1fx over capacity)\n\n",
			shortfall, requiredHotStorageGB/availableStorageGB)

		fmt.Println("📋 Recommended Actions (choose one or combine):")
		fmt.Println()

		// Option 1: Reduce retention
		for _, days := range []int{7, 5, 3} {
			reducedHot := float64(estimate.AvgCompressedSize*int64(days)*int64(estimate.SitesScanned)) / (1024 * 1024 * 1024)
			if reducedHot <= availableStorageGB {
				fmt.Printf("1️⃣  REDUCE RETENTION to %d daily backups\n", days)
				fmt.Printf("   Required: %.2f GB (%.1f%% of available)\n", reducedHot, (reducedHot/availableStorageGB)*100)
				fmt.Printf("   Trade-off: Less recovery granularity\n")
				fmt.Printf("   Command: --daily-retention %d\n", days)
				fmt.Println()
				break
			}
		}

		// Option 2: Faster glacier migration
		for _, days := range []int{7, 5, 3, 2, 1} {
			if days < estimate.DailyRetention {
				reducedHot := float64(estimate.AvgCompressedSize*int64(days)*int64(estimate.SitesScanned)) / (1024 * 1024 * 1024)
				if reducedHot <= availableStorageGB {
					fmt.Printf("2️⃣  MIGRATE FASTER to Glacier (keep %d days hot, rest in Glacier)\n", days)
					fmt.Printf("   Required: %.2f GB (%.1f%% of available)\n", reducedHot, (reducedHot/availableStorageGB)*100)
					fmt.Printf("   Trade-off: Higher retrieval costs if needed\n")
					fmt.Printf("   Action: Run monitor more frequently, migrate after %d days\n", days)
					fmt.Println()
					break
				}
			}
		}

		// Option 3: Expand storage
		recommendedExpansion := requiredHotStorageGB * 1.2 // 20% buffer
		fmt.Printf("3️⃣  EXPAND STORAGE to %.0f GB minimum\n", recommendedExpansion)
		fmt.Printf("   Additional needed: %.0f GB\n", recommendedExpansion-availableStorageGB)
		fmt.Printf("   Benefit: Maintain current %d-day retention policy\n", estimate.DailyRetention)
		fmt.Printf("   For future growth: Consider %.0f GB\n", recommendedExpansion*1.5)
		fmt.Println()

		// Option 4: Reduce site count (less common)
		if estimate.SitesScanned > 100 {
			maxSites := int(availableStorageGB / (float64(estimate.AvgCompressedSize*int64(estimate.DailyRetention)) / (1024 * 1024 * 1024)))
			fmt.Printf("4️⃣  REDUCE ACTIVE SITES to ~%d sites\n", maxSites)
			fmt.Printf("   Archive/disable %d sites\n", estimate.SitesScanned-maxSites)
			fmt.Printf("   Trade-off: Service fewer sites\n")
			fmt.Println()
		}

	} else if utilizationPct > 80 {
		// WARNING: High utilization
		fmt.Printf("⚠️  WARNING: High storage utilization (%.1f%% of capacity)\n\n", utilizationPct)
		fmt.Println("📋 Recommendations:")
		fmt.Println()
		fmt.Printf("  • Monitor storage closely - only %.2f GB headroom\n", availableStorageGB-requiredHotStorageGB)
		fmt.Printf("  • Plan for expansion if growth rate >%.1f%% monthly\n", (20.0/utilizationPct)*100)
		fmt.Printf("  • Consider migrating to Glacier after %d days instead of %d\n",
			estimate.DailyRetention-3, estimate.DailyRetention)
		fmt.Println()

	} else {
		// SUFFICIENT CAPACITY
		fmt.Printf("✓ SUFFICIENT: Storage utilization at %.1f%% of capacity\n\n", utilizationPct)
		headroom := availableStorageGB - requiredHotStorageGB
		monthsOfGrowth := 0
		if estimate.GrowthProjections != nil && len(estimate.GrowthProjections) > 0 {
			for i, proj := range estimate.GrowthProjections {
				if proj.HotStorageGB > availableStorageGB {
					monthsOfGrowth = i
					break
				}
			}
		}

		fmt.Println("📊 Capacity Summary:")
		fmt.Printf("  • Headroom: %.2f GB (%.1f%% free)\n", headroom, ((availableStorageGB-requiredHotStorageGB)/availableStorageGB)*100)
		if monthsOfGrowth > 0 {
			fmt.Printf("  • Growth capacity: ~%d months at current rate\n", monthsOfGrowth)
			fmt.Printf("  • Review capacity in %d months\n", monthsOfGrowth-3)
		}
		fmt.Printf("  • Current policy sustainable: %d daily + %d weekly + %d monthly\n",
			estimate.DailyRetention, estimate.WeeklyRetention, estimate.MonthlyRetention)
		fmt.Println()
	}
}
