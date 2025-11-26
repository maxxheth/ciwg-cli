package auth

import (
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// SSHClient represents an SSH connection
type SSHClient struct {
	client   *ssh.Client
	agent    agent.Agent
	hostname string
	username string
}

// SSHConfig represents SSH connection configuration
type SSHConfig struct {
	Hostname           string
	Username           string
	Port               string
	KeyPath            string
	UseAgent           bool
	Timeout            time.Duration
	KeepAlive          time.Duration
	DisableDefaultKeys bool
}

// NewSSHClient creates a new SSH client with persistent connection and agent support
func NewSSHClient(config SSHConfig) (*SSHClient, error) {
	if config.Port == "" {
		config.Port = "22"
	}
	if config.Timeout == 0 {
		config.Timeout = 30 * time.Second
	}
	if config.KeepAlive == 0 {
		config.KeepAlive = 30 * time.Second
	}

	var authMethods []ssh.AuthMethod

	// Try SSH agent first if available
	if config.UseAgent {
		if agentAuth, err := getSSHAgent(); err == nil {
			authMethods = append(authMethods, agentAuth)
		}
	}

	// Try key-based authentication
	if config.KeyPath != "" {
		if keyAuth, err := getPublicKeyAuth(config.KeyPath); err == nil {
			authMethods = append(authMethods, keyAuth)
		}
	}

	if !config.DisableDefaultKeys {
		defaultKeys := []string{
			filepath.Join(os.Getenv("HOME"), ".ssh", "id_rsa"),
			filepath.Join(os.Getenv("HOME"), ".ssh", "id_ed25519"),
			filepath.Join(os.Getenv("HOME"), ".ssh", "id_ecdsa"),
		}

		for _, keyPath := range defaultKeys {
			if config.KeyPath != "" && filepath.Clean(keyPath) == filepath.Clean(config.KeyPath) {
				continue // already added explicitly
			}
			if _, err := os.Stat(keyPath); err == nil {
				if keyAuth, err := getPublicKeyAuth(keyPath); err == nil {
					authMethods = append(authMethods, keyAuth)
				}
			}
		}
	}

	if len(authMethods) == 0 {
		return nil, fmt.Errorf("no valid authentication methods found")
	}

	sshConfig := &ssh.ClientConfig{
		User:            config.Username,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // In production, use proper host key verification
		Timeout:         config.Timeout,
	}

	address := net.JoinHostPort(config.Hostname, config.Port)
	client, err := ssh.Dial("tcp", address, sshConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to %s: %w", address, err)
	}

	// Set up keep-alive
	go func() {
		ticker := time.NewTicker(config.KeepAlive)
		defer ticker.Stop()
		for range ticker.C {
			_, _, err := client.SendRequest("keepalive@openssh.com", true, nil)
			if err != nil {
				return
			}
		}
	}()

	sshClient := &SSHClient{
		client:   client,
		hostname: config.Hostname,
		username: config.Username,
	}

	// Store SSH agent if available
	if config.UseAgent {
		if agentConn, err := net.Dial("unix", os.Getenv("SSH_AUTH_SOCK")); err == nil {
			sshClient.agent = agent.NewClient(agentConn)
		}
	}

	return sshClient, nil
}

// getSSHAgent returns SSH agent authentication method
func getSSHAgent() (ssh.AuthMethod, error) {
	socket := os.Getenv("SSH_AUTH_SOCK")
	if socket == "" {
		return nil, fmt.Errorf("SSH_AUTH_SOCK not set")
	}

	conn, err := net.Dial("unix", socket)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to SSH agent: %w", err)
	}

	agentClient := agent.NewClient(conn)
	return ssh.PublicKeysCallback(agentClient.Signers), nil
}

// getPublicKeyAuth returns public key authentication method
func getPublicKeyAuth(keyPath string) (ssh.AuthMethod, error) {
	key, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read private key: %w", err)
	}

	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("failed to parse private key: %w", err)
	}

	return ssh.PublicKeys(signer), nil
}

// ExecuteCommand executes a command on the remote server
func (c *SSHClient) ExecuteCommand(command string) (string, string, error) {
	session, err := c.client.NewSession()
	if err != nil {
		return "", "", fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	var stdout, stderr io.Writer
	var stdoutBuf, stderrBuf []byte

	stdout = &bytesBuffer{&stdoutBuf}
	stderr = &bytesBuffer{&stderrBuf}

	session.Stdout = stdout
	session.Stderr = stderr

	err = session.Run(command)
	return string(stdoutBuf), string(stderrBuf), err
}

// GetSession returns a new SSH session for more complex operations like piping.
func (c *SSHClient) GetSession() (*ssh.Session, error) {
	if c.client == nil {
		return nil, fmt.Errorf("ssh client is not connected")
	}
	session, err := c.client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("failed to create session: %w", err)
	}
	return session, nil
}

// ExecuteInteractiveCommand executes a command with real-time output
func (c *SSHClient) ExecuteInteractiveCommand(command string, stdout, stderr io.Writer) error {
	session, err := c.client.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	session.Stdout = stdout
	session.Stderr = stderr

	return session.Run(command)
}

// CopyFile copies a file to the remote server using SCP
func (c *SSHClient) CopyFile(localPath, remotePath string) error {
	session, err := c.client.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	file, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("failed to open local file: %w", err)
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat file: %w", err)
	}

	stdin, err := session.StdinPipe()
	if err != nil {
		return fmt.Errorf("failed to get stdin pipe: %w", err)
	}

	go func() {
		defer stdin.Close()
		fmt.Fprintf(stdin, "C%04o %d %s\n", stat.Mode().Perm(), stat.Size(), filepath.Base(remotePath))
		io.Copy(stdin, file)
		fmt.Fprint(stdin, "\x00")
	}()

	return session.Run(fmt.Sprintf("scp -qt %s", filepath.Dir(remotePath)))
}

// IsAlive checks if the SSH connection is still active
func (c *SSHClient) IsAlive() bool {
	session, err := c.client.NewSession()
	if err != nil {
		return false
	}
	defer session.Close()

	return session.Run("true") == nil
}

// Close closes the SSH connection
func (c *SSHClient) Close() error {
	if c.client != nil {
		return c.client.Close()
	}
	return nil
}

// GetHostname returns the hostname of the connection
func (c *SSHClient) GetHostname() string {
	return c.hostname
}

// GetUsername returns the username of the connection
func (c *SSHClient) GetUsername() string {
	return c.username
}

// bytesBuffer is a simple implementation of io.Writer for capturing output
type bytesBuffer struct {
	bytes *[]byte
}

func (b *bytesBuffer) Write(p []byte) (int, error) {
	*b.bytes = append(*b.bytes, p...)
	return len(p), nil
}
