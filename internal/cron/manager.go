package cron

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"ciwg-cli/internal/auth"
)

// CronJob represents a cron job entry
type CronJob struct {
	Schedule string
	Command  string
	User     string
	Comment  string
	Enabled  bool
	ID       string
	LastRun  *time.Time
	NextRun  *time.Time
}

// CronManager handles cron job operations
type CronManager struct {
	sshClient *auth.SSHClient
}

// NewCronManager creates a new cron manager
func NewCronManager(sshClient *auth.SSHClient) *CronManager {
	return &CronManager{
		sshClient: sshClient,
	}
}

// ListCronJobs lists all cron jobs on the remote system
func (cm *CronManager) ListCronJobs() ([]CronJob, error) {
	// Get cron jobs for the current user
	stdout, stderr, err := cm.sshClient.ExecuteCommand("crontab -l 2>/dev/null || echo ''")
	if err != nil && stderr != "" {
		return nil, fmt.Errorf("failed to list cron jobs: %s", stderr)
	}

	jobs := cm.parseCronOutput(stdout, cm.sshClient.GetUsername())

	// Try to get system-wide cron jobs (requires appropriate permissions)
	systemOut, _, err := cm.sshClient.ExecuteCommand("ls /etc/cron.d/ 2>/dev/null || echo ''")
	if err == nil && systemOut != "" {
		systemJobs, _ := cm.getSystemCronJobs()
		jobs = append(jobs, systemJobs...)
	}

	return jobs, nil
}

// parseCronOutput parses crontab output into CronJob structs
func (cm *CronManager) parseCronOutput(output, user string) []CronJob {
	var jobs []CronJob
	scanner := bufio.NewScanner(strings.NewReader(output))

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		job := cm.parseCronLine(line, user)
		if job != nil {
			jobs = append(jobs, *job)
		}
	}

	return jobs
}

// parseCronLine parses a single cron line
func (cm *CronManager) parseCronLine(line, user string) *CronJob {
	// Regular cron format: minute hour day month weekday command
	parts := strings.Fields(line)
	if len(parts) < 6 {
		return nil
	}

	schedule := strings.Join(parts[0:5], " ")
	command := strings.Join(parts[5:], " ")

	// Generate a simple ID based on the line content
	id := fmt.Sprintf("%x", line)[:8]

	job := &CronJob{
		Schedule: schedule,
		Command:  command,
		User:     user,
		Enabled:  true,
		ID:       id,
	}

	// Calculate next run time
	if nextRun := cm.calculateNextRun(schedule); nextRun != nil {
		job.NextRun = nextRun
	}

	return job
}

// getSystemCronJobs gets system-wide cron jobs
func (cm *CronManager) getSystemCronJobs() ([]CronJob, error) {
	var jobs []CronJob

	// Check /etc/cron.d/
	stdout, _, err := cm.sshClient.ExecuteCommand("find /etc/cron.d/ -type f -exec cat {} \\; 2>/dev/null || echo ''")
	if err == nil {
		systemJobs := cm.parseCronOutput(stdout, "system")
		jobs = append(jobs, systemJobs...)
	}

	return jobs, nil
}

// AddCronJob adds a new cron job.
// If cronJob is not an empty string, it's added non-interactively.
// Otherwise, it runs in interactive mode.
func (cm *CronManager) AddCronJob(cronJob string) error {
	if cronJob != "" {
		// Non-interactive mode: add the provided cron job string directly.
		return cm.addToCrontab(cronJob)
	}

	// Interactive mode
	fmt.Println("=== Add New Cron Job ===")

	// Get schedule
	schedule, err := cm.getScheduleInput()
	if err != nil {
		return err
	}

	// Get command
	fmt.Print("Enter the command to execute: ")
	reader := bufio.NewReader(os.Stdin)
	command, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("failed to read command: %w", err)
	}
	command = strings.TrimSpace(command)

	if command == "" {
		return fmt.Errorf("command cannot be empty")
	}

	// Get optional comment
	fmt.Print("Enter a comment (optional): ")
	comment, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("failed to read comment: %w", err)
	}
	comment = strings.TrimSpace(comment)

	// Create the cron entry
	cronLine := fmt.Sprintf("%s %s", schedule, command)
	if comment != "" {
		cronLine = fmt.Sprintf("# %s\n%s", comment, cronLine)
	}

	// Add to crontab
	return cm.addToCrontab(cronLine)
}

// getScheduleInput gets cron schedule from user input
func (cm *CronManager) getScheduleInput() (string, error) {
	fmt.Println("Choose schedule type:")
	fmt.Println("1. Predefined (hourly, daily, weekly, monthly)")
	fmt.Println("2. Custom cron expression")

	reader := bufio.NewReader(os.Stdin)
	fmt.Print("Enter choice (1 or 2): ")
	choice, err := reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("failed to read choice: %w", err)
	}
	choice = strings.TrimSpace(choice)

	switch choice {
	case "1":
		return cm.getPredefinedSchedule()
	case "2":
		return cm.getCustomSchedule()
	default:
		return "", fmt.Errorf("invalid choice")
	}
}

// getPredefinedSchedule gets a predefined schedule
func (cm *CronManager) getPredefinedSchedule() (string, error) {
	schedules := map[string]string{
		"1": "0 * * * *",    // hourly
		"2": "0 0 * * *",    // daily
		"3": "0 0 * * 0",    // weekly
		"4": "0 0 1 * *",    // monthly
		"5": "*/5 * * * *",  // every 5 minutes
		"6": "*/15 * * * *", // every 15 minutes
		"7": "*/30 * * * *", // every 30 minutes
	}

	fmt.Println("Select schedule:")
	fmt.Println("1. Hourly (at minute 0)")
	fmt.Println("2. Daily (at midnight)")
	fmt.Println("3. Weekly (Sunday at midnight)")
	fmt.Println("4. Monthly (1st day at midnight)")
	fmt.Println("5. Every 5 minutes")
	fmt.Println("6. Every 15 minutes")
	fmt.Println("7. Every 30 minutes")

	reader := bufio.NewReader(os.Stdin)
	fmt.Print("Enter choice (1-7): ")
	choice, err := reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("failed to read choice: %w", err)
	}
	choice = strings.TrimSpace(choice)

	if schedule, ok := schedules[choice]; ok {
		return schedule, nil
	}
	return "", fmt.Errorf("invalid choice")
}

// getCustomSchedule gets a custom cron schedule
func (cm *CronManager) getCustomSchedule() (string, error) {
	fmt.Println("Enter cron expression (minute hour day month weekday):")
	fmt.Println("Example: 0 2 * * 1 (every Monday at 2 AM)")

	reader := bufio.NewReader(os.Stdin)
	fmt.Print("Cron expression: ")
	schedule, err := reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("failed to read schedule: %w", err)
	}
	schedule = strings.TrimSpace(schedule)

	// Basic validation
	parts := strings.Fields(schedule)
	if len(parts) != 5 {
		return "", fmt.Errorf("cron expression must have exactly 5 parts")
	}

	return schedule, nil
}

// addToCrontab adds a line to the user's crontab
func (cm *CronManager) addToCrontab(cronLine string) error {
	// Get current crontab
	currentCrontab, _, _ := cm.sshClient.ExecuteCommand("crontab -l 2>/dev/null || echo ''")

	// Backup current crontab to $HOME/crontab-backup.txt
	backupCmd := "crontab -l 2>/dev/null > \"$HOME/crontab-backup.txt\""
	_, backupErr, err := cm.sshClient.ExecuteCommand(backupCmd)
	if err != nil {
		fmt.Printf("Warning: failed to backup crontab: %s\n", backupErr)
	}

	// Add new line
	newCrontab := currentCrontab
	if !strings.HasSuffix(newCrontab, "\n") && newCrontab != "" {
		newCrontab += "\n"
	}
	newCrontab += cronLine + "\n"

	// Write back to crontab (use heredoc to preserve formatting)
	cmd := fmt.Sprintf("cat <<'EOF' | crontab -\n%s\nEOF", newCrontab)
	_, stderr, err := cm.sshClient.ExecuteCommand(cmd)
	if err != nil {
		return fmt.Errorf("failed to update crontab: %s", stderr)
	}

	fmt.Println("Cron job added successfully!")
	return nil
}

// RemoveCronJob removes a cron job by ID
func (cm *CronManager) RemoveCronJob(jobID string) error {
	jobs, err := cm.ListCronJobs()
	if err != nil {
		return err
	}

	var jobToRemove *CronJob
	for _, job := range jobs {
		if job.ID == jobID {
			jobToRemove = &job
			break
		}
	}

	if jobToRemove == nil {
		return fmt.Errorf("cron job with ID %s not found", jobID)
	}

	// Backup current crontab to $HOME/crontab-backup.txt
	backupCmd := "crontab -l 2>/dev/null > \"$HOME/crontab-backup.txt\""
	_, backupErr, err := cm.sshClient.ExecuteCommand(backupCmd)
	if err != nil {
		fmt.Printf("Warning: failed to backup crontab: %s\n", backupErr)
	}

	// Get current crontab
	currentCrontab, _, _ := cm.sshClient.ExecuteCommand("crontab -l 2>/dev/null || echo ''")

	// Remove the job line
	lines := strings.Split(currentCrontab, "\n")
	var newLines []string

	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			job := cm.parseCronLine(strings.TrimSpace(line), cm.sshClient.GetUsername())
			if job == nil || job.ID != jobID {
				newLines = append(newLines, line)
			}
		}
	}

	newCrontab := strings.Join(newLines, "\n")
	if newCrontab != "" && !strings.HasSuffix(newCrontab, "\n") {
		newCrontab += "\n"
	}

	// Write back to crontab (use heredoc to preserve formatting)
	cmd := fmt.Sprintf("cat <<'EOF' | crontab -\n%s\nEOF", newCrontab)
	_, stderr, err := cm.sshClient.ExecuteCommand(cmd)
	if err != nil {
		return fmt.Errorf("failed to update crontab: %s", stderr)
	}

	fmt.Printf("Cron job %s removed successfully!\n", jobID)
	return nil
}

// calculateNextRun calculates the next run time for a cron schedule
func (cm *CronManager) calculateNextRun(schedule string) *time.Time {
	// This is a simplified calculation - in production, use a proper cron parser
	parts := strings.Fields(schedule)
	if len(parts) != 5 {
		return nil
	}

	now := time.Now()

	// For simplicity, just add an hour for any schedule
	// In a real implementation, you'd parse the cron expression properly
	nextRun := now.Add(time.Hour)
	return &nextRun
}

// ValidateCronExpression validates a cron expression
func ValidateCronExpression(expr string) error {
	parts := strings.Fields(expr)
	if len(parts) != 5 {
		return fmt.Errorf("cron expression must have exactly 5 parts, got %d", len(parts))
	}

	validators := []struct {
		name string
		min  int
		max  int
	}{
		{"minute", 0, 59},
		{"hour", 0, 23},
		{"day", 1, 31},
		{"month", 1, 12},
		{"weekday", 0, 7}, // 0 and 7 are both Sunday
	}

	for i, part := range parts {
		if part == "*" {
			continue
		}

		// Handle step values (*/5)
		if strings.Contains(part, "/") {
			stepParts := strings.Split(part, "/")
			if len(stepParts) != 2 {
				return fmt.Errorf("invalid step format in %s: %s", validators[i].name, part)
			}

			if stepParts[0] != "*" {
				if err := validateRange(stepParts[0], validators[i].min, validators[i].max); err != nil {
					return fmt.Errorf("invalid %s range: %w", validators[i].name, err)
				}
			}

			stepVal, err := strconv.Atoi(stepParts[1])
			if err != nil || stepVal <= 0 {
				return fmt.Errorf("invalid step value in %s: %s", validators[i].name, stepParts[1])
			}
			continue
		}

		// Handle ranges (1-5)
		if strings.Contains(part, "-") {
			rangeParts := strings.Split(part, "-")
			if len(rangeParts) != 2 {
				return fmt.Errorf("invalid range format in %s: %s", validators[i].name, part)
			}

			for _, rangePart := range rangeParts {
				if err := validateRange(rangePart, validators[i].min, validators[i].max); err != nil {
					return fmt.Errorf("invalid %s range: %w", validators[i].name, err)
				}
			}
			continue
		}

		// Handle comma-separated values (1,3,5)
		if strings.Contains(part, ",") {
			valueParts := strings.Split(part, ",")
			for _, valuePart := range valueParts {
				if err := validateRange(valuePart, validators[i].min, validators[i].max); err != nil {
					return fmt.Errorf("invalid %s value: %w", validators[i].name, err)
				}
			}
			continue
		}

		// Single value
		if err := validateRange(part, validators[i].min, validators[i].max); err != nil {
			return fmt.Errorf("invalid %s value: %w", validators[i].name, err)
		}
	}

	return nil
}

// validateRange validates a single value is within the allowed range
func validateRange(value string, min, max int) error {
	num, err := strconv.Atoi(value)
	if err != nil {
		return fmt.Errorf("not a valid number: %s", value)
	}

	if num < min || num > max {
		return fmt.Errorf("value %d is outside allowed range [%d-%d]", num, min, max)
	}

	return nil
}
