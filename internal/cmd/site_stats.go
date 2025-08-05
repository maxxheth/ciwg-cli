package cmd

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	l "log"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// SiteStats holds the extracted statistics for a single WordPress site.
type SiteStats struct {
	Server           string         `json:"server"`
	Container        string         `json:"container"`
	Domain           string         `json:"domain"`
	PostCount        int            `json:"post_count"`
	PageCount        int            `json:"page_count"`
	CustomPostCounts map[string]int `json:"custom_post_counts"`
	SiteAge          string         `json:"site_age"`
	SiteAgeDays      int            `json:"site_age_days"`
}

var siteStatsCmd = &cobra.Command{
	Use:   "site-stats",
	Short: "Extract statistics like content count and age from WordPress sites.",
	Long: `Scans Docker containers on one or more servers to extract WordPress site statistics.
This includes counts for posts, pages, and custom post types, as well as the age of the site based on its database creation date.`,
	RunE: runSiteStats,
}

func init() {
	rootCmd.AddCommand(siteStatsCmd)

	// Command-specific flags
	siteStatsCmd.Flags().StringVarP(&serverRange, "server-range", "s", "local", "Server range (e.g., 'local', 'wp%d.ciwgserver.com:1-14')")
	siteStatsCmd.Flags().StringVarP(&outputFile, "output", "o", "", "Output file (default: stdout)")
	siteStatsCmd.Flags().StringVarP(&outputFormat, "format", "f", "csv", "Output format (csv or json)")

	// SSH connection flags (reusing the pattern from other commands)
	siteStatsCmd.Flags().StringP("user", "u", "", "SSH username (default: current user)")
	siteStatsCmd.Flags().StringP("port", "p", "22", "SSH port")
	siteStatsCmd.Flags().StringP("key", "k", "", "Path to SSH private key")
	siteStatsCmd.Flags().BoolP("agent", "a", true, "Use SSH agent")
	siteStatsCmd.Flags().DurationP("timeout", "t", 30*time.Second, "Connection timeout")
}

func runSiteStats(cmd *cobra.Command, args []string) error {
	pattern, start, end, err := parseServerRange(serverRange)
	if err != nil {
		return fmt.Errorf("error parsing server range: %w", err)
	}

	var servers []string
	if serverRange == "local" {
		servers = []string{"local"}
	} else {
		for i := start; i <= end; i++ {
			servers = append(servers, fmt.Sprintf(pattern, i))
		}
	}

	allStats := []SiteStats{}
	l.Printf("Starting statistics extraction from %d servers...", len(servers))

	for i, server := range servers {
		l.Printf("[%d/%d] Processing server: %s", i+1, len(servers), server)
		stats, err := extractStatsFromServer(cmd, server)
		if err != nil {
			l.Printf("Could not extract stats from server %s: %v", server, err)
			continue
		}
		allStats = append(allStats, stats...)
	}

	l.Printf("Extraction complete. Found stats for %d sites. Writing output...", len(allStats))
	return outputStats(allStats)
}

func extractStatsFromServer(cmd *cobra.Command, server string) ([]SiteStats, error) {
	containers, err := getWordPressContainers(cmd, server)
	if err != nil {
		return nil, fmt.Errorf("failed to get containers from %s: %w", server, err)
	}

	l.Printf("  > Found %d containers. Extracting stats...", len(containers))
	serverStats := []SiteStats{}
	for _, container := range containers {
		stats, err := extractStatsFromContainer(cmd, server, container)
		if err != nil {
			l.Printf("    ! Failed to get stats from container %s: %v", container, err)
			continue
		}
		serverStats = append(serverStats, *stats)
	}
	return serverStats, nil
}

func executeWPCommand(cmd *cobra.Command, server, command string) (string, error) {
	if server == "local" {
		execCmd := exec.Command("sh", "-c", command)
		output, err := execCmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("local command failed: %w - %s", err, string(output))
		}
		return string(output), nil
	}

	client, err := createSSHClient(cmd, server)
	if err != nil {
		return "", err
	}
	defer client.Close()

	stdout, stderr, err := client.ExecuteCommand(command)
	if err != nil {
		return "", fmt.Errorf("ssh command failed: %w - %s", err, stderr)
	}
	return stdout, nil
}

func extractStatsFromContainer(cmd *cobra.Command, server, container string) (*SiteStats, error) {
	stats := &SiteStats{
		Server:           server,
		Container:        container,
		Domain:           strings.TrimPrefix(container, "wp_"),
		CustomPostCounts: make(map[string]int),
	}

	// Get post and page counts
	postCountCmd := fmt.Sprintf("docker exec %s wp post list --post_type=post --format=count --allow-root", container)
	pageCountCmd := fmt.Sprintf("docker exec %s wp post list --post_type=page --format=count --allow-root", container)

	postCountStr, err := executeWPCommand(cmd, server, postCountCmd)
	if err == nil {
		stats.PostCount, _ = strconv.Atoi(strings.TrimSpace(postCountStr))
	}

	pageCountStr, err := executeWPCommand(cmd, server, pageCountCmd)
	if err == nil {
		stats.PageCount, _ = strconv.Atoi(strings.TrimSpace(pageCountStr))
	}

	// Get custom post types and their counts
	cptListCmd := fmt.Sprintf("docker exec %s wp post-type list --field=name --allow-root", container)
	cptListStr, err := executeWPCommand(cmd, server, cptListCmd)
	if err == nil {
		cpts := strings.Split(strings.TrimSpace(cptListStr), "\n")
		for _, cpt := range cpts {
			// Filter out default WordPress post types
			if !isDefaultPostType(cpt) {
				cptCountCmd := fmt.Sprintf("docker exec %s wp post list --post_type=%s --format=count --allow-root", container, cpt)
				cptCountStr, err := executeWPCommand(cmd, server, cptCountCmd)
				if err == nil {
					stats.CustomPostCounts[cpt], _ = strconv.Atoi(strings.TrimSpace(cptCountStr))
				}
			}
		}
	}

	// Get site age
	ageQuery := `wp db query "SELECT MIN(CREATE_TIME) AS Oldest_Table, DATEDIFF(NOW(), MIN(CREATE_TIME)) AS Age_In_Days FROM information_schema.TABLES WHERE table_schema = '$(wp db name)';" --allow-root`
	ageCmd := fmt.Sprintf("docker exec %s %s", container, ageQuery)
	ageOutput, err := executeWPCommand(cmd, server, ageCmd)
	if err == nil {
		stats.SiteAgeDays = parseAgeInDays(ageOutput)
		stats.SiteAge = formatDaysToYearsMonths(stats.SiteAgeDays)
	}

	return stats, nil
}

func isDefaultPostType(cpt string) bool {
	defaults := map[string]bool{
		"post":                true,
		"page":                true,
		"attachment":          true,
		"revision":            true,
		"nav_menu_item":       true,
		"custom_css":          true,
		"customize_changeset": true,
		"oembed_cache":        true,
		"user_request":        true,
		"wp_block":            true,
	}
	return defaults[cpt]
}

func parseAgeInDays(dbOutput string) int {
	re := regexp.MustCompile(`\s*(\d+)\s*\|`)
	lines := strings.Split(dbOutput, "\n")
	if len(lines) > 3 {
		matches := re.FindStringSubmatch(lines[3])
		if len(matches) > 1 {
			days, _ := strconv.Atoi(matches[1])
			return days
		}
	}
	return 0
}

func formatDaysToYearsMonths(days int) string {
	if days <= 0 {
		return "Less than a month"
	}
	years := days / 365
	remainingDays := days % 365
	months := remainingDays / 30

	if years == 0 {
		return fmt.Sprintf("%d months", months)
	}
	return fmt.Sprintf("%d years, %d months", years, months)
}

func outputStats(allStats []SiteStats) error {
	var writer *os.File
	var err error

	if outputFile == "" {
		writer = os.Stdout
	} else {
		writer, err = os.Create(outputFile)
		if err != nil {
			return fmt.Errorf("failed to create output file %s: %w", outputFile, err)
		}
		defer writer.Close()
	}

	switch outputFormat {
	case "json":
		encoder := json.NewEncoder(writer)
		encoder.SetIndent("", "  ")
		return encoder.Encode(allStats)
	case "csv":
		csvWriter := csv.NewWriter(writer)
		defer csvWriter.Flush()

		header := []string{"server", "domain", "posts", "pages", "custom_posts", "site_age", "site_age_days"}
		if err := csvWriter.Write(header); err != nil {
			return err
		}

		for _, stats := range allStats {
			cptStrs := []string{}
			for cpt, count := range stats.CustomPostCounts {
				cptStrs = append(cptStrs, fmt.Sprintf("%s:%d", cpt, count))
			}

			record := []string{
				stats.Server,
				stats.Domain,
				strconv.Itoa(stats.PostCount),
				strconv.Itoa(stats.PageCount),
				strings.Join(cptStrs, ";"),
				stats.SiteAge,
				strconv.Itoa(stats.SiteAgeDays),
			}
			if err := csvWriter.Write(record); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unsupported format: %s", outputFormat)
	}
}
