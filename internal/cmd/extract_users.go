package cmd

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	l "log"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

type User struct {
	Email        string   `json:"email"`
	IDs          []string `json:"ids"`
	Usernames    []string `json:"usernames"`
	DisplayNames []string `json:"display_names"`
	Roles        []string `json:"roles"`
	Registered   []string `json:"registered"`
	Servers      []string `json:"servers"`
	Containers   []string `json:"containers"`
}

var (
	outputFormat string
)

var extractUsersCmd = &cobra.Command{
	Use:   "extract-users",
	Short: "Extract WordPress users from Docker containers",
	Long: `Extract WordPress users from Docker containers across multiple servers.
Users are deduplicated by email address, with other fields collapsed into comma-delimited strings.`,
	RunE: runExtractUsers,
}

func init() {
	rootCmd.AddCommand(extractUsersCmd)

	extractUsersCmd.Flags().StringVarP(&outputFormat, "format", "f", "csv", "Output format (csv or json)")
	extractUsersCmd.Flags().StringVarP(&serverRange, "server-range", "s", "local", "Server range (e.g., 'local', 'wp%d.ciwgserver.com:1-14')")
	extractUsersCmd.Flags().StringVarP(&outputFile, "output", "o", "", "Output file (default: stdout)")

	// Add SSH connection flags, like in cron.go
	extractUsersCmd.Flags().StringP("user", "u", "", "SSH username (default: current user)")
	extractUsersCmd.Flags().StringP("port", "p", "22", "SSH port")
	extractUsersCmd.Flags().StringP("key", "k", "", "Path to SSH private key")
	extractUsersCmd.Flags().BoolP("agent", "a", true, "Use SSH agent")
	extractUsersCmd.Flags().DurationP("timeout", "t", 30*time.Second, "Connection timeout")
}

func runExtractUsers(cmd *cobra.Command, args []string) error {
	pattern, start, end, exclusions, err := parseServerRange(serverRange)
	if err != nil {
		return fmt.Errorf("error parsing server range: %w", err)
	}

	var servers []string
	if serverRange == "local" {
		servers = []string{"local"}
	} else {
		for i := start; i <= end; i++ {
			if exclusions[i] {
				fmt.Printf("Skipping excluded server: %s\n", fmt.Sprintf(pattern, i))
				continue
			}
			servers = append(servers, fmt.Sprintf(pattern, i))
		}
	}

	userMap := make(map[string]*User)

	l.Printf("Starting user extraction from %d servers...", len(servers))
	for i, server := range servers {
		l.Printf("[%d/%d] Processing server: %s", i+1, len(servers), server)
		// Pass the cmd object down
		if err := extractFromServer(cmd, server, userMap); err != nil {
			l.Printf("error extracting from server %s: %v\n", server, err)
		}
	}

	l.Printf("Extraction complete. Found %d unique users. Writing output...", len(userMap))
	return outputUsers(userMap)
}

// Pass cmd to use for creating the SSH client
func extractFromServer(cmd *cobra.Command, server string, userMap map[string]*User) error {
	containers, err := getWordPressContainers(cmd, server)
	if err != nil {
		l.Printf("Warning: Failed to get containers from %s: %v\n", server, err)
		return nil // Continue with other servers even if one fails
	}

	l.Printf("  > Found %d containers. Extracting users...", len(containers))
	for _, container := range containers {
		if err := extractUsersFromContainer(cmd, server, container, userMap); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Failed to extract users from %s on %s: %v\n", container, server, err)
			continue
		}
	}

	return nil
}

// Pass cmd to use for creating the SSH client
func getWordPressContainers(cmd *cobra.Command, server string) ([]string, error) {
	var output []byte
	var err error

	if server == "local" {
		execCmd := exec.Command("docker", "ps", "--format", "{{.Names}}")
		output, err = execCmd.Output()
	} else {
		// Use SSH client for remote servers
		client, err := createSSHClient(cmd, server) // Use the proper client creation function
		if err != nil {
			return nil, fmt.Errorf("failed to connect to %s: %w", server, err)
		}
		defer client.Close()

		stdout, stderr, err := client.ExecuteCommand("docker ps --format '{{.Names}}'")
		if err != nil {
			return nil, fmt.Errorf("failed to list containers: %w (stderr: %s)", err, stderr)
		}
		output = []byte(stdout)
	}

	if err != nil {
		return nil, err
	}

	var containers []string
	lines := strings.SplitSeq(string(output), "\n")

	for line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "wp_") {
			containers = append(containers, line)
		}
	}

	return containers, nil
}

// Pass cmd to use for creating the SSH client
func extractUsersFromContainer(cmd *cobra.Command, server, container string, userMap map[string]*User) error {
	var output []byte
	var err error

	wpCmd := fmt.Sprintf("docker exec %s wp user list --format=json --fields=ID,user_login,user_email,display_name,roles,user_registered --allow-root", container)

	if server == "local" {
		execCmd := exec.Command("sh", "-c", wpCmd)
		output, err = execCmd.Output()
	} else {
		// Use SSH client for remote servers
		client, err := createSSHClient(cmd, server) // Use the proper client creation function
		if err != nil {
			return fmt.Errorf("failed to connect to %s: %w", server, err)
		}
		defer client.Close()

		stdout, stderr, err := client.ExecuteCommand(wpCmd)
		if err != nil {
			return fmt.Errorf("failed to extract users: %w (stderr: %s)", err, stderr)
		}
		output = []byte(stdout)
	}

	if err != nil {
		return err
	}

	var wpUsers []struct {
		ID             json.Number `json:"ID"`
		UserLogin      string      `json:"user_login"`
		UserEmail      string      `json:"user_email"`
		DisplayName    string      `json:"display_name"`
		Roles          string      `json:"roles"`
		UserRegistered string      `json:"user_registered"`
	}

	// Use a decoder to handle numbers as strings
	decoder := json.NewDecoder(strings.NewReader(string(output)))
	decoder.UseNumber()

	if err := decoder.Decode(&wpUsers); err != nil {
		return err
	}

	for _, wpUser := range wpUsers {
		email := strings.ToLower(strings.TrimSpace(wpUser.UserEmail))
		if email == "" {
			continue
		}

		if _, exists := userMap[email]; !exists {
			userMap[email] = &User{
				Email:        email,
				IDs:          []string{},
				Usernames:    []string{},
				DisplayNames: []string{},
				Roles:        []string{},
				Registered:   []string{},
				Servers:      []string{},
				Containers:   []string{},
			}
		}

		user := userMap[email]

		// Add unique values
		addUnique(&user.IDs, wpUser.ID.String())
		addUnique(&user.Usernames, wpUser.UserLogin)
		addUnique(&user.DisplayNames, wpUser.DisplayName)
		addUnique(&user.Roles, wpUser.Roles)
		addUnique(&user.Registered, wpUser.UserRegistered)
		addUnique(&user.Servers, server)
		addUnique(&user.Containers, container)
	}

	return nil
}

func addUnique(slice *[]string, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}

	for _, v := range *slice {
		if v == value {
			return
		}
	}
	*slice = append(*slice, value)
}

func outputUsers(userMap map[string]*User) error {
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
		return outputJSON(writer, userMap)
	case "csv":
		return outputCSV(writer, userMap)
	default:
		return fmt.Errorf("unsupported format: %s", outputFormat)
	}
}

func outputJSON(writer *os.File, userMap map[string]*User) error {
	users := make([]*User, 0, len(userMap))
	for _, user := range userMap {
		users = append(users, user)
	}

	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(users)
}

func outputCSV(writer *os.File, userMap map[string]*User) error {
	csvWriter := csv.NewWriter(writer)
	defer csvWriter.Flush()

	// Write header
	header := []string{"email", "ids", "usernames", "display_names", "roles", "registered", "servers", "containers"}
	if err := csvWriter.Write(header); err != nil {
		return err
	}

	// Write data
	for _, user := range userMap {
		record := []string{
			user.Email,
			strings.Join(user.IDs, ","),
			strings.Join(user.Usernames, ","),
			strings.Join(user.DisplayNames, ","),
			strings.Join(user.Roles, ","),
			strings.Join(user.Registered, ","),
			strings.Join(user.Servers, ","),
			strings.Join(user.Containers, ","),
		}
		if err := csvWriter.Write(record); err != nil {
			return err
		}
	}

	return nil
}
