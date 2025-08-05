package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ciwg-cli/internal/auth"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/spf13/cobra"
)

type ContainerInfo struct {
	Domain  string `json:"domain"`
	Website string `json:"website"`
	Server  string `json:"server"`
	IP      string `json:"ip"`
}

var (
	inventoryOutputFile  string
	inventoryServerRange string
	inventoryLocal       bool
)

var inventoryCmd = &cobra.Command{
	Use:   "inventory",
	Short: "Generate inventory of WordPress containers",
	Long:  `Scan Docker containers and generate an inventory of WordPress sites with their domains, URLs, and server information.`,
}

var inventoryGenerateCmd = &cobra.Command{
	Use:   "generate [user@]host",
	Short: "Generate inventory of WordPress containers on specified server(s)",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runInventoryGenerate,
}

func init() {
	rootCmd.AddCommand(inventoryCmd)
	inventoryCmd.AddCommand(inventoryGenerateCmd)

	// Inventory specific flags
	inventoryGenerateCmd.Flags().StringVarP(&inventoryOutputFile, "output", "o", "inventory.json", "Output file for inventory JSON")
	inventoryGenerateCmd.Flags().StringVar(&inventoryServerRange, "server-range", "", "Server range pattern (e.g., 'wp%d.ciwgserver.com:0-41')")
	inventoryGenerateCmd.Flags().BoolVar(&inventoryLocal, "local", false, "Run locally without SSH")

	// SSH connection flags
	inventoryGenerateCmd.Flags().StringP("user", "u", "", "SSH username (default: current user)")
	inventoryGenerateCmd.Flags().StringP("port", "p", "22", "SSH port")
	inventoryGenerateCmd.Flags().StringP("key", "k", "", "Path to SSH private key")
	inventoryGenerateCmd.Flags().BoolP("agent", "a", true, "Use SSH agent")
	inventoryGenerateCmd.Flags().DurationP("timeout", "t", 30*time.Second, "Connection timeout")
}

func runInventoryGenerate(cmd *cobra.Command, args []string) error {
	var allInventory []ContainerInfo

	if inventoryServerRange != "" {
		// Process multiple servers
		pattern, start, end, err := parseServerRange(inventoryServerRange)
		if err != nil {
			return fmt.Errorf("error parsing server range: %w", err)
		}

		for i := start; i <= end; i++ {
			serverHost := fmt.Sprintf(pattern, i)
			fmt.Printf("Processing server: %s\n", serverHost)

			inventory, err := processServer(cmd, serverHost)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error processing %s: %v\n", serverHost, err)
				continue
			}
			allInventory = append(allInventory, inventory...)
		}
	} else if inventoryLocal {
		// Process locally
		fmt.Println("Processing local server...")
		inventory, err := processLocalServer()
		if err != nil {
			return fmt.Errorf("error processing local server: %w", err)
		}
		allInventory = append(allInventory, inventory...)
	} else {
		// Process single remote server
		if len(args) < 1 {
			return fmt.Errorf("remote mode requires [user@]host argument")
		}
		inventory, err := processServer(cmd, args[0])
		if err != nil {
			return fmt.Errorf("error processing server: %w", err)
		}
		allInventory = append(allInventory, inventory...)
	}

	// Write results to JSON file
	jsonData, err := json.MarshalIndent(allInventory, "", "  ")
	if err != nil {
		return fmt.Errorf("error marshaling inventory to JSON: %w", err)
	}

	if err := os.WriteFile(inventoryOutputFile, jsonData, 0644); err != nil {
		return fmt.Errorf("error writing inventory file: %w", err)
	}

	fmt.Printf("Inventory written to %s\n", inventoryOutputFile)
	fmt.Printf("Total containers found: %d\n", len(allInventory))
	return nil
}

func processServer(cmd *cobra.Command, serverHost string) ([]ContainerInfo, error) {
	// Create SSH client
	sshClient, err := createSSHClient(cmd, serverHost)
	if err != nil {
		return nil, fmt.Errorf("error creating SSH client: %w", err)
	}
	defer sshClient.Close()

	// Get server public IP
	serverIP, err := getServerPublicIP(sshClient)
	if err != nil {
		return nil, fmt.Errorf("error getting server IP: %w", err)
	}

	// Get hostname
	hostname, err := getServerHostname(sshClient)
	if err != nil {
		return nil, fmt.Errorf("error getting hostname: %w", err)
	}

	// Get container information via Docker API over SSH
	inventory, err := getContainersViaSSH(sshClient, hostname, serverIP)
	if err != nil {
		return nil, fmt.Errorf("error getting containers: %w", err)
	}

	return inventory, nil
}

func processLocalServer() ([]ContainerInfo, error) {
	// Get local public IP
	serverIP, err := getLocalServerIP()
	if err != nil {
		return nil, fmt.Errorf("error getting local IP: %w", err)
	}

	// Get local hostname
	hostname, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("error getting hostname: %w", err)
	}

	// Get container information via local Docker API
	inventory, err := getContainersLocal(hostname, serverIP)
	if err != nil {
		return nil, fmt.Errorf("error getting containers: %w", err)
	}

	return inventory, nil
}

func getServerPublicIP(client *auth.SSHClient) (string, error) {
	cmd := "dig +short myip.opendns.com @resolver1.opendns.com"
	stdout, stderr, err := client.ExecuteCommand(cmd)
	if err != nil && len(stderr) > 0 {
		// Fallback to curl
		cmd = "curl -s https://api.ipify.org"
		stdout, stderr, err = client.ExecuteCommand(cmd)
		if err != nil {
			return "", fmt.Errorf("failed to get public IP: %w, stderr: %s", err, stderr)
		}
	}
	return strings.TrimSpace(stdout), nil
}

func getServerHostname(client *auth.SSHClient) (string, error) {
	stdout, stderr, err := client.ExecuteCommand("hostname")
	if err != nil {
		return "", fmt.Errorf("failed to get hostname: %w, stderr: %s", err, stderr)
	}
	return strings.TrimSpace(stdout), nil
}

func getLocalServerIP() (string, error) {
	// Try multiple methods to get public IP
	cmds := []string{
		"dig +short myip.opendns.com @resolver1.opendns.com",
		"curl -s https://api.ipify.org",
	}
	for _, cmd := range cmds {
		out, err := runLocalCommand(cmd)
		if err == nil && strings.TrimSpace(out) != "" {
			return strings.TrimSpace(out), nil
		}
	}
	return "", fmt.Errorf("could not determine public IP")
}

func getContainersViaSSH(sshClient *auth.SSHClient, hostname, serverIP string) ([]ContainerInfo, error) {
	var inventory []ContainerInfo

	// Use docker CLI via SSH since Docker SDK over SSH is complex
	// First, get all wp_ containers
	stdout, stderr, err := sshClient.ExecuteCommand("docker ps --format='{{.Names}}' | grep '^wp_'")
	if err != nil {
		if strings.Contains(stderr, "No such container") || stdout == "" {
			// No containers found
			return inventory, nil
		}
		return nil, fmt.Errorf("failed to list containers: %w, stderr: %s", err, stderr)
	}

	containers := strings.Split(strings.TrimSpace(stdout), "\n")

	for _, containerName := range containers {
		if containerName == "" {
			continue
		}

		// Get container inspect data
		inspectCmd := fmt.Sprintf("docker inspect %s", containerName)
		inspectOut, _, err := sshClient.ExecuteCommand(inspectCmd)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error inspecting container %s: %v\n", containerName, err)
			continue
		}

		fmt.Printf("Inspect output for %s:\n%s\n", containerName, inspectOut)

		// Parse WP_HOME from environment variables
		wpHome := ""
		wpHomeCmd := fmt.Sprintf(`docker inspect %s | jq -r '.[].Config.Env | map(select(contains("WP_HOME="))) | .[0] | split("=")[1]'`, containerName)
		wpHomeOut, _, err := sshClient.ExecuteCommand(wpHomeCmd)
		if err == nil {
			wpHome = strings.TrimSpace(wpHomeOut)
		}

		// Get working directory (domain)
		domainCmd := fmt.Sprintf(`docker inspect %s | jq -r '.[].Config.Labels."com.docker.compose.project.working_dir"'`, containerName)
		domainOut, _, err := sshClient.ExecuteCommand(domainCmd)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting domain for container %s: %v\n", containerName, err)
			continue
		}

		workingDir := strings.TrimSpace(domainOut)
		domain := filepath.Base(workingDir)

		// Create container info
		info := ContainerInfo{
			Domain:  domain,
			Website: wpHome,
			Server:  hostname,
			IP:      serverIP,
		}

		inventory = append(inventory, info)
	}

	return inventory, nil
}

func getContainersLocal(hostname, serverIP string) ([]ContainerInfo, error) {
	var inventory []ContainerInfo

	// Create Docker client
	ctx := context.Background()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("failed to create Docker client: %w", err)
	}
	defer cli.Close()

	// List containers
	containers, err := cli.ContainerList(ctx, container.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}

	for _, container := range containers {
		// Check if container name starts with wp_
		containerName := ""
		for _, name := range container.Names {
			name = strings.TrimPrefix(name, "/")
			if strings.HasPrefix(name, "wp_") {
				containerName = name
				break
			}
		}

		if containerName == "" {
			continue
		}

		// Inspect container
		inspect, err := cli.ContainerInspect(ctx, container.ID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error inspecting container %s: %v\n", containerName, err)
			continue
		}

		// Find WP_HOME in environment variables
		wpHome := ""
		for _, env := range inspect.Config.Env {
			if strings.HasPrefix(env, "WP_HOME=") {
				wpHome = strings.TrimPrefix(env, "WP_HOME=")
				break
			}
		}

		// Get working directory from labels
		workingDir := inspect.Config.Labels["com.docker.compose.project.working_dir"]
		domain := filepath.Base(workingDir)

		// Create container info
		info := ContainerInfo{
			Domain:  domain,
			Website: wpHome,
			Server:  hostname,
			IP:      serverIP,
		}

		inventory = append(inventory, info)
	}

	return inventory, nil
}

// Parse server range pattern (e.g., 'wp%d.ciwgserver.com:0-41')
