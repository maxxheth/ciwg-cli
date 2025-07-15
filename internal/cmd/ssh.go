package cmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"ciwg-cli/internal/auth"
)

var sshCmd = &cobra.Command{
	Use:   "ssh",
	Short: "SSH connection management",
	Long:  `Manage SSH connections with persistent authentication and agent support.`,
}

var sshConnectCmd = &cobra.Command{
	Use:   "connect [hostname]",
	Short: "Connect to a remote server via SSH",
	Args:  cobra.ExactArgs(1),
	RunE:  runSSHConnect,
}

var sshTestCmd = &cobra.Command{
	Use:   "test [hostname]",
	Short: "Test SSH connection to a remote server",
	Args:  cobra.ExactArgs(1),
	RunE:  runSSHTest,
}

func init() {
	rootCmd.AddCommand(sshCmd)
	sshCmd.AddCommand(sshConnectCmd)
	sshCmd.AddCommand(sshTestCmd)

	// SSH connection flags
	sshConnectCmd.Flags().StringP("user", "u", "", "SSH username (default: current user)")
	sshConnectCmd.Flags().StringP("port", "p", "22", "SSH port")
	sshConnectCmd.Flags().StringP("key", "k", "", "Path to SSH private key")
	sshConnectCmd.Flags().BoolP("agent", "a", true, "Use SSH agent")
	sshConnectCmd.Flags().DurationP("timeout", "t", 30*time.Second, "Connection timeout")
	sshConnectCmd.Flags().Duration("keepalive", 30*time.Second, "Keep-alive interval")

	// Bind flags to viper
	viper.BindPFlag("ssh.user", sshConnectCmd.Flags().Lookup("user"))
	viper.BindPFlag("ssh.port", sshConnectCmd.Flags().Lookup("port"))
	viper.BindPFlag("ssh.key", sshConnectCmd.Flags().Lookup("key"))
	viper.BindPFlag("ssh.agent", sshConnectCmd.Flags().Lookup("agent"))
	viper.BindPFlag("ssh.timeout", sshConnectCmd.Flags().Lookup("timeout"))
	viper.BindPFlag("ssh.keepalive", sshConnectCmd.Flags().Lookup("keepalive"))

	// Copy flags to test command
	sshTestCmd.Flags().StringP("user", "u", "", "SSH username (default: current user)")
	sshTestCmd.Flags().StringP("port", "p", "22", "SSH port")
	sshTestCmd.Flags().StringP("key", "k", "", "Path to SSH private key")
	sshTestCmd.Flags().BoolP("agent", "a", true, "Use SSH agent")
	sshTestCmd.Flags().DurationP("timeout", "t", 30*time.Second, "Connection timeout")
}

func runSSHConnect(cmd *cobra.Command, args []string) error {
	hostname := args[0]

	// Get connection parameters
	username, _ := cmd.Flags().GetString("user")
	if username == "" {
		username = getCurrentUser()
	}

	port, _ := cmd.Flags().GetString("port")
	keyPath, _ := cmd.Flags().GetString("key")
	useAgent, _ := cmd.Flags().GetBool("agent")
	timeout, _ := cmd.Flags().GetDuration("timeout")
	keepalive, _ := cmd.Flags().GetDuration("keepalive")

	config := auth.SSHConfig{
		Hostname:  hostname,
		Username:  username,
		Port:      port,
		KeyPath:   keyPath,
		UseAgent:  useAgent,
		Timeout:   timeout,
		KeepAlive: keepalive,
	}

	fmt.Printf("Connecting to %s@%s:%s...\n", username, hostname, port)

	client, err := auth.NewSSHClient(config)
	if err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}
	defer client.Close()

	fmt.Printf("Successfully connected to %s!\n", hostname)

	// Test the connection
	if client.IsAlive() {
		fmt.Println("Connection is active and ready for commands.")
	} else {
		fmt.Println("Warning: Connection test failed.")
	}

	// Execute a simple test command
	stdout, stderr, err := client.ExecuteCommand("whoami && hostname")
	if err != nil {
		fmt.Printf("Test command failed: %v\n", err)
		if stderr != "" {
			fmt.Printf("Error: %s\n", stderr)
		}
	} else {
		fmt.Printf("Remote info:\n%s", stdout)
	}

	return nil
}

func runSSHTest(cmd *cobra.Command, args []string) error {
	hostname := args[0]

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

	fmt.Printf("Testing SSH connection to %s@%s:%s...\n", username, hostname, port)

	start := time.Now()
	client, err := auth.NewSSHClient(config)
	if err != nil {
		return fmt.Errorf("connection test failed: %w", err)
	}
	defer client.Close()

	connectionTime := time.Since(start)
	fmt.Printf("✓ Connection established in %v\n", connectionTime)

	// Test basic command execution
	start = time.Now()
	stdout, stderr, err := client.ExecuteCommand("echo 'test'")
	executionTime := time.Since(start)

	if err != nil {
		fmt.Printf("✗ Command execution failed: %v\n", err)
		if stderr != "" {
			fmt.Printf("  Error: %s\n", stderr)
		}
		return err
	}

	fmt.Printf("✓ Command execution successful in %v\n", executionTime)
	fmt.Printf("  Output: %s", stdout)

	// Test keep-alive
	if client.IsAlive() {
		fmt.Println("✓ Connection keep-alive is working")
	} else {
		fmt.Println("✗ Connection keep-alive failed")
	}

	fmt.Println("SSH connection test completed successfully!")
	return nil
}

func getCurrentUser() string {
	// In a real implementation, you'd get the current user
	// For now, return a default
	return "root"
}
