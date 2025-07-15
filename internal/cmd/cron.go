package cmd

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"ciwg-cli/internal/auth"
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
	Args:  cobra.ExactArgs(1),
	RunE:  runCronList,
}

var cronAddCmd = &cobra.Command{
	Use:   "add [hostname]",
	Short: "Add a new cron job interactively",
	Args:  cobra.ExactArgs(1),
	RunE:  runCronAdd,
}

var cronRemoveCmd = &cobra.Command{
	Use:   "remove [hostname] [job-id]",
	Short: "Remove a cron job by ID",
	Args:  cobra.ExactArgs(2),
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
	hostname := args[0]

	sshClient, err := createSSHClient(cmd, hostname)
	if err != nil {
		return err
	}
	defer sshClient.Close()

	cronManager := cron.NewCronManager(sshClient)

	fmt.Printf("Listing cron jobs on %s...\n\n", hostname)

	jobs, err := cronManager.ListCronJobs()
	if err != nil {
		return fmt.Errorf("failed to list cron jobs: %w", err)
	}

	if len(jobs) == 0 {
		fmt.Println("No cron jobs found.")
		return nil
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
	hostname := args[0]

	sshClient, err := createSSHClient(cmd, hostname)
	if err != nil {
		return err
	}
	defer sshClient.Close()

	cronManager := cron.NewCronManager(sshClient)

	fmt.Printf("Adding cron job to %s...\n\n", hostname)

	return cronManager.AddCronJob()
}

func runCronRemove(cmd *cobra.Command, args []string) error {
	hostname := args[0]
	jobID := args[1]

	sshClient, err := createSSHClient(cmd, hostname)
	if err != nil {
		return err
	}
	defer sshClient.Close()

	cronManager := cron.NewCronManager(sshClient)

	fmt.Printf("Removing cron job %s from %s...\n", jobID, hostname)

	return cronManager.RemoveCronJob(jobID)
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

func createSSHClient(cmd *cobra.Command, hostname string) (*auth.SSHClient, error) {
	// Get connection parameters
	username, _ := cmd.Flags().GetString("user")
	if username == "" {
		username = getCurrentUser()
	}

	port, _ := cmd.Flags().GetString("port")
	keyPath, _ := cmd.Flags().GetString("key")
	useAgent, _ := cmd.Flags().GetBool("agent")
	timeout, _ := cmd.Flags().GetDuration("timeout")

	config := auth.SSHConfig{
		Hostname:  hostname,
		Username:  username,
		Port:      port,
		KeyPath:   keyPath,
		UseAgent:  useAgent,
		Timeout:   timeout,
		KeepAlive: 30 * time.Second,
	}

	return auth.NewSSHClient(config)
}
