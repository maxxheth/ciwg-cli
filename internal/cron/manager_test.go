package cron

import (
	"strings"
	"testing"
	"time"
)

func TestValidateCronExpression(t *testing.T) {
	validExpressions := []string{
		"0 0 * * *",      // daily at midnight
		"*/5 * * * *",    // every 5 minutes
		"0 9-17 * * 1-5", // weekdays 9-5
		"0 0 1 * *",      // monthly
		"15,45 * * * *",  // at 15 and 45 minutes
	}

	for _, expr := range validExpressions {
		t.Run("valid_"+strings.ReplaceAll(expr, " ", "_"), func(t *testing.T) {
			if err := ValidateCronExpression(expr); err != nil {
				t.Errorf("Expected valid expression %q to pass validation, but got error: %v", expr, err)
			}
		})
	}

	invalidExpressions := []struct {
		expr string
		desc string
	}{
		{"0 0 * *", "too few parts"},
		{"0 0 * * * *", "too many parts"},
		{"60 0 * * *", "invalid minute"},
		{"0 25 * * *", "invalid hour"},
		{"0 0 32 * *", "invalid day"},
		{"0 0 * 13 *", "invalid month"},
		{"0 0 * * 8", "invalid weekday"},
		{"*/0 * * * *", "invalid step"},
		{"a b c d e", "non-numeric values"},
	}

	for _, test := range invalidExpressions {
		t.Run("invalid_"+test.desc, func(t *testing.T) {
			if err := ValidateCronExpression(test.expr); err == nil {
				t.Errorf("Expected invalid expression %q to fail validation", test.expr)
			}
		})
	}
}

func TestValidateRange(t *testing.T) {
	tests := []struct {
		value    string
		min      int
		max      int
		expected bool
	}{
		{"0", 0, 59, true},
		{"59", 0, 59, true},
		{"30", 0, 59, true},
		{"60", 0, 59, false},
		{"-1", 0, 59, false},
		{"abc", 0, 59, false},
		{"", 0, 59, false},
	}

	for _, test := range tests {
		t.Run(test.value, func(t *testing.T) {
			err := validateRange(test.value, test.min, test.max)
			if test.expected && err != nil {
				t.Errorf("Expected %q to be valid, but got error: %v", test.value, err)
			}
			if !test.expected && err == nil {
				t.Errorf("Expected %q to be invalid, but it passed validation", test.value)
			}
		})
	}
}

func TestParseCronLine(t *testing.T) {
	cm := &CronManager{}

	tests := []struct {
		line     string
		user     string
		expected *CronJob
	}{
		{
			line: "0 2 * * * /usr/bin/backup.sh",
			user: "testuser",
			expected: &CronJob{
				Schedule: "0 2 * * *",
				Command:  "/usr/bin/backup.sh",
				User:     "testuser",
				Enabled:  true,
			},
		},
		{
			line: "*/15 * * * * echo 'hello world'",
			user: "testuser",
			expected: &CronJob{
				Schedule: "*/15 * * * *",
				Command:  "echo 'hello world'",
				User:     "testuser",
				Enabled:  true,
			},
		},
		{
			line:     "invalid line",
			user:     "testuser",
			expected: nil,
		},
		{
			line:     "0 0",
			user:     "testuser",
			expected: nil,
		},
	}

	for _, test := range tests {
		t.Run(test.line, func(t *testing.T) {
			result := cm.parseCronLine(test.line, test.user)

			if test.expected == nil {
				if result != nil {
					t.Errorf("Expected nil result for line %q, but got: %+v", test.line, result)
				}
				return
			}

			if result == nil {
				t.Errorf("Expected valid result for line %q, but got nil", test.line)
				return
			}

			if result.Schedule != test.expected.Schedule {
				t.Errorf("Expected schedule %q, got %q", test.expected.Schedule, result.Schedule)
			}
			if result.Command != test.expected.Command {
				t.Errorf("Expected command %q, got %q", test.expected.Command, result.Command)
			}
			if result.User != test.expected.User {
				t.Errorf("Expected user %q, got %q", test.expected.User, result.User)
			}
			if result.Enabled != test.expected.Enabled {
				t.Errorf("Expected enabled %v, got %v", test.expected.Enabled, result.Enabled)
			}
		})
	}
}

func TestParseCronOutput(t *testing.T) {
	cm := &CronManager{}

	output := `# This is a comment
0 2 * * * /usr/bin/backup.sh
*/15 * * * * /usr/bin/monitor.sh

# Another comment
0 0 * * 0 /usr/bin/weekly.sh`

	jobs := cm.parseCronOutput(output, "testuser")

	if len(jobs) != 3 {
		t.Errorf("Expected 3 jobs, got %d", len(jobs))
	}

	expectedCommands := []string{
		"/usr/bin/backup.sh",
		"/usr/bin/monitor.sh",
		"/usr/bin/weekly.sh",
	}

	for i, job := range jobs {
		if job.Command != expectedCommands[i] {
			t.Errorf("Expected command %q, got %q", expectedCommands[i], job.Command)
		}
		if job.User != "testuser" {
			t.Errorf("Expected user 'testuser', got %q", job.User)
		}
		if !job.Enabled {
			t.Errorf("Expected job to be enabled")
		}
	}
}

func TestCronJob_Structure(t *testing.T) {
	now := time.Now()
	nextRun := now.Add(time.Hour)

	job := CronJob{
		Schedule: "0 * * * *",
		Command:  "echo 'test'",
		User:     "testuser",
		Comment:  "Test job",
		Enabled:  true,
		ID:       "test123",
		LastRun:  &now,
		NextRun:  &nextRun,
	}

	if job.Schedule != "0 * * * *" {
		t.Errorf("Schedule not set correctly")
	}
	if job.Command != "echo 'test'" {
		t.Errorf("Command not set correctly")
	}
	if job.User != "testuser" {
		t.Errorf("User not set correctly")
	}
	if job.Comment != "Test job" {
		t.Errorf("Comment not set correctly")
	}
	if !job.Enabled {
		t.Errorf("Enabled not set correctly")
	}
	if job.ID != "test123" {
		t.Errorf("ID not set correctly")
	}
	if job.LastRun == nil || !job.LastRun.Equal(now) {
		t.Errorf("LastRun not set correctly")
	}
	if job.NextRun == nil || !job.NextRun.Equal(nextRun) {
		t.Errorf("NextRun not set correctly")
	}
}
