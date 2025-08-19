package cmd

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"ciwg-cli/internal/cron"
)

var cronCmd = &cobra.Command{
	Use:   "cron",
	Short: "Cron job management",
	Long:  `Manage cron jobs on remote servers with interactive tools.`,
}

var cronListCmd = &cobra.Command{
	Use:   "list [hostname]",
	Short: "List all cron jobs on the remote server",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runCronList,
}

var cronAddCmd = &cobra.Command{
	Use:   "add [hostname]",
	Short: "Add a new cron job interactively to one or more servers",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runCronAdd,
}

var cronRemoveCmd = &cobra.Command{
	Use:   "remove [hostname] [job-id]",
	Short: "Remove cron job(s) by ID or pattern from one or more servers",
	Args:  cobra.MaximumNArgs(2),
	RunE:  runCronRemove,
}

var cronValidateCmd = &cobra.Command{
	Use:   "validate [expression]",
	Short: "Validate a cron expression",
	Args:  cobra.ExactArgs(1),
	RunE:  runCronValidate,
}

func init() {
	rootCmd.AddCommand(cronCmd)
	cronCmd.AddCommand(cronListCmd)
	cronCmd.AddCommand(cronAddCmd)
	cronCmd.AddCommand(cronRemoveCmd)
	cronCmd.AddCommand(cronValidateCmd)

	cronListCmd.Flags().String("grep", "", "Filter cron jobs by command pattern")
	cronListCmd.Flags().String("id", "", "Filter cron jobs by ID")
	cronListCmd.Flags().Bool("remove", false, "Remove found cron jobs")
	cronListCmd.Flags().String("server-range", "", "Server range pattern (e.g., 'wp%d.example.com:0-41')")

	// Add server-range to add and remove commands
	cronAddCmd.Flags().String("server-range", "", "Server range pattern (e.g., 'wp%d.example.com:0-41')")
	cronAddCmd.Flags().String("cron-job", "", "The full cron job string to add non-interactively (e.g., '* * * * * /usr/bin/true')")
	cronRemoveCmd.Flags().String("server-range", "", "Server range pattern (e.g., 'wp%d.example.com:0-41')")
	cronRemoveCmd.Flags().String("grep", "", "Filter cron jobs by command pattern")
	cronRemoveCmd.Flags().String("id", "", "Filter cron jobs by ID")

	// Add SSH connection flags to cron commands
	for _, cmd := range []*cobra.Command{cronListCmd, cronAddCmd, cronRemoveCmd} {
		cmd.Flags().StringP("user", "u", "", "SSH username (default: current user)")
		cmd.Flags().StringP("port", "p", "22", "SSH port")
		cmd.Flags().StringP("key", "k", "", "Path to SSH private key")
		cmd.Flags().BoolP("agent", "a", true, "Use SSH agent")
		cmd.Flags().DurationP("timeout", "t", 30*time.Second, "Connection timeout")
	}
}

func runCronList(cmd *cobra.Command, args []string) error {
	serverRange, _ := cmd.Flags().GetString("server-range")

	if serverRange != "" {
		return processCronListForServerRange(cmd, serverRange)
	}

	if len(args) < 1 {
		return fmt.Errorf("hostname argument is required when --server-range is not used")
	}

	hostname := args[0]
	return listCronsForHost(cmd, hostname)
}

func processCronListForServerRange(cmd *cobra.Command, serverRange string) error {
	pattern, start, end, exclusions, err := parseServerRange(serverRange)
	if err != nil {
		return fmt.Errorf("error parsing server range: %w", err)
	}

	for i := start; i <= end; i++ {
		if exclusions[i] {
			fmt.Printf("Skipping excluded server: %s\n", fmt.Sprintf(pattern, i))
			continue
		}
		hostname := fmt.Sprintf(pattern, i)
		fmt.Printf("--- Processing server: %s ---\n", hostname)
		err := listCronsForHost(cmd, hostname)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error processing %s: %v\n", hostname, err)
		}
		fmt.Println()
	}

	return nil
}

func listCronsForHost(cmd *cobra.Command, hostname string) error {
	sshClient, err := createSSHClient(cmd, hostname)
	if err != nil {
		return err
	}
	defer sshClient.Close()

	cronManager := cron.NewCronManager(sshClient)

	grepPattern, _ := cmd.Flags().GetString("grep")
	jobID, _ := cmd.Flags().GetString("id")
	remove, _ := cmd.Flags().GetBool("remove")

	jobs, err := cronManager.ListCronJobs()
	if err != nil {
		return fmt.Errorf("failed to list cron jobs: %w", err)
	}

	var filteredJobs []cron.CronJob
	if grepPattern != "" || jobID != "" {
		for _, job := range jobs {
			match := true
			if jobID != "" && job.ID != jobID {
				match = false
			}
			if grepPattern != "" && !strings.Contains(job.Command, grepPattern) {
				match = false
			}
			if match {
				filteredJobs = append(filteredJobs, job)
			}
		}
		jobs = filteredJobs
	}

	if len(jobs) == 0 {
		fmt.Println("No cron jobs found matching the criteria.")
		return nil
	}

	if remove {
		fmt.Printf("Found %d cron job(s) to remove on %s.\n", len(jobs), hostname)
		for _, job := range jobs {
			fmt.Printf("Removing job ID %s...", job.ID)
			if err := cronManager.RemoveCronJob(job.ID); err != nil {
				fmt.Printf(" failed: %v\n", err)
			} else {
				fmt.Println(" success.")
			}
		}
		return nil
	}

	if grepPattern != "" {
		fmt.Printf("Listing cron jobs on %s matching \"%s\"...\n\n", hostname, grepPattern)
	} else {
		fmt.Printf("Listing cron jobs on %s...\n\n", hostname)
	}

	// Display jobs in a formatted table
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tSCHEDULE\tUSER\tCOMMAND\tNEXT RUN")
	fmt.Fprintln(w, "--\t--------\t----\t-------\t--------")

	for _, job := range jobs {
		nextRun := "N/A"
		if job.NextRun != nil {
			nextRun = job.NextRun.Format("2006-01-02 15:04")
		}

		command := job.Command
		if len(command) > 50 {
			command = command[:47] + "..."
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			job.ID, job.Schedule, job.User, command, nextRun)
	}

	w.Flush()

	fmt.Printf("\nTotal: %d cron jobs\n", len(jobs))
	return nil
}

func runCronAdd(cmd *cobra.Command, args []string) error {
	serverRange, _ := cmd.Flags().GetString("server-range")

	if serverRange != "" {
		return processCronAddForServerRange(cmd, serverRange)
	}

	if len(args) < 1 {
		return fmt.Errorf("hostname argument is required when --server-range is not used")
	}

	hostname := args[0]
	return addCronForHost(cmd, hostname)
}

func processCronAddForServerRange(cmd *cobra.Command, serverRange string) error {
	pattern, start, end, exclusions, err := parseServerRange(serverRange)
	if err != nil {
		return fmt.Errorf("error parsing server range: %w", err)
	}

	// It's assumed AddCronJob is interactive. We will prompt for each server.
	for i := start; i <= end; i++ {
		if exclusions[i] {
			fmt.Printf("Skipping excluded server: %s\n", fmt.Sprintf(pattern, i))
			continue
		}
		hostname := fmt.Sprintf(pattern, i)
		fmt.Printf("--- Processing server: %s ---\n", hostname)
		err := addCronForHost(cmd, hostname)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error processing %s: %v\n", hostname, err)
		}
		fmt.Println()
	}

	return nil
}

func addCronForHost(cmd *cobra.Command, hostname string) error {
	sshClient, err := createSSHClient(cmd, hostname)
	if err != nil {
		return err
	}
	defer sshClient.Close()

	cronManager := cron.NewCronManager(sshClient)

	cronJobString, _ := cmd.Flags().GetString("cron-job")

	// If a cron job string is provided, add it non-interactively.
	// Note: This assumes `AddCronJob` is modified to accept a string.
	// An empty string will trigger the interactive mode.
	if cronJobString != "" {
		fmt.Printf("Adding cron job to %s: %s\n", hostname, cronJobString)
		err := cronManager.AddCronJob(cronJobString)
		if err != nil {
			fmt.Printf("Failed to add cron job: %v\n", err)
		} else {
			fmt.Println("Success.")
		}
		return err
	}

	// Otherwise, run in interactive mode.
	fmt.Printf("Adding cron job to %s (interactive)...\n\n", hostname)
	return cronManager.AddCronJob("")
}

func runCronRemove(cmd *cobra.Command, args []string) error {
	serverRange, _ := cmd.Flags().GetString("server-range")
	grepPattern, _ := cmd.Flags().GetString("grep")
	jobIDFilter, _ := cmd.Flags().GetString("id")

	// If using filtering patterns, we don't need a job-id argument
	hasFilters := grepPattern != "" || jobIDFilter != ""

	if serverRange != "" {
		if !hasFilters && len(args) < 1 {
			return fmt.Errorf("job-id argument is required when --server-range is used without --grep or --id filters")
		}
		var jobID string
		if len(args) > 0 {
			jobID = args[0]
		}
		return processCronRemoveForServerRange(cmd, serverRange, jobID)
	}

	if len(args) < 1 {
		return fmt.Errorf("hostname argument is required when --server-range is not used")
	}

	if !hasFilters && len(args) < 2 {
		return fmt.Errorf("job-id argument is required when --grep and --id filters are not used")
	}

	hostname := args[0]
	var jobID string
	if len(args) > 1 {
		jobID = args[1]
	}
	return removeCronForHost(cmd, hostname, jobID)
}

func processCronRemoveForServerRange(cmd *cobra.Command, serverRange, jobID string) error {
	pattern, start, end, exclusions, err := parseServerRange(serverRange)
	if err != nil {
		return fmt.Errorf("error parsing server range: %w", err)
	}

	for i := start; i <= end; i++ {
		if exclusions[i] {
			fmt.Printf("Skipping excluded server: %s\n", fmt.Sprintf(pattern, i))
			continue
		}
		hostname := fmt.Sprintf(pattern, i)
		fmt.Printf("--- Processing server: %s ---\n", hostname)
		err := removeCronForHost(cmd, hostname, jobID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error processing %s: %v\n", hostname, err)
		}
		fmt.Println()
	}

	return nil
}

func removeCronForHost(cmd *cobra.Command, hostname, jobID string) error {
	sshClient, err := createSSHClient(cmd, hostname)
	if err != nil {
		return err
	}
	defer sshClient.Close()

	cronManager := cron.NewCronManager(sshClient)

	grepPattern, _ := cmd.Flags().GetString("grep")
	jobIDFilter, _ := cmd.Flags().GetString("id")

	// If using filters instead of direct job ID
	if grepPattern != "" || jobIDFilter != "" {
		jobs, err := cronManager.ListCronJobs()
		if err != nil {
			return fmt.Errorf("failed to list cron jobs: %w", err)
		}

		var jobsToRemove []cron.CronJob
		for _, job := range jobs {
			match := true
			if jobIDFilter != "" && job.ID != jobIDFilter {
				match = false
			}
			if grepPattern != "" && !strings.Contains(job.Command, grepPattern) {
				match = false
			}
			if match {
				jobsToRemove = append(jobsToRemove, job)
			}
		}

		if len(jobsToRemove) == 0 {
			fmt.Printf("No cron jobs found matching the criteria on %s.\n", hostname)
			return nil
		}

		fmt.Printf("Found %d cron job(s) to remove on %s:\n", len(jobsToRemove), hostname)
		for _, job := range jobsToRemove {
			fmt.Printf("  ID: %s, Command: %s\n", job.ID, job.Command)
		}

		for _, job := range jobsToRemove {
			fmt.Printf("Removing job ID %s...", job.ID)
			if err := cronManager.RemoveCronJob(job.ID); err != nil {
				fmt.Printf(" failed: %v\n", err)
			} else {
				fmt.Println(" success.")
			}
		}
		return nil
	}

	// Direct job ID removal (existing functionality)
	fmt.Printf("Removing cron job %s from %s...\n", jobID, hostname)

	err = cronManager.RemoveCronJob(jobID)
	if err == nil {
		fmt.Println("Success.")
	}
	return err
}

func runCronValidate(cmd *cobra.Command, args []string) error {
	expression := args[0]

	fmt.Printf("Validating cron expression: %s\n", expression)

	err := cron.ValidateCronExpression(expression)
	if err != nil {
		fmt.Printf("✗ Invalid cron expression: %v\n", err)
		return err
	}

	fmt.Println("✓ Valid cron expression")

	// Show what the expression means
	parts := []string{
		"minute", "hour", "day of month", "month", "day of week",
	}

	fields := strings.Fields(expression)
	fmt.Println("\nExpression breakdown:")
	for i, field := range fields {
		fmt.Printf("  %s: %s\n", parts[i], field)
	}

	return nil
}
