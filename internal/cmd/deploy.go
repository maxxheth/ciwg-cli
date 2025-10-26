package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
)

var deployCmd = &cobra.Command{
	Use:   "deploy [hostname]",
	Short: "Deploy ciwg-cli package to one or more servers",
	Long:  "Upload and install the ciwg-cli package on remote servers. Supports a single hostname or --server-range.",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runDeploy,
}

func init() {
	rootCmd.AddCommand(deployCmd)

	deployCmd.Flags().String("tarball", "", "Local tarball path to upload (required)")
	deployCmd.Flags().String("remote-dir", "/usr/local/bin/ciwg-cli-utils", "Remote directory to extract into")
	deployCmd.Flags().Int("strip-components", 1, "Number of leading path components to strip when extracting tarball")
	deployCmd.Flags().Bool("start-containers", false, "If true, attempt to run docker compose up -d in the remote install dir after deploy")
	deployCmd.Flags().Bool("build", false, "Run 'make build' locally before uploading the tarball")
	deployCmd.Flags().Bool("dry-run", false, "Show actions without executing them")
	deployCmd.Flags().String("server-range", "", "Server range pattern (e.g., 'wp%d.example.com:0-41')")

	// SSH flags (same pattern as other commands)
	deployCmd.Flags().StringP("user", "u", "", "SSH username (default: current user)")
	deployCmd.Flags().StringP("port", "p", "22", "SSH port")
	deployCmd.Flags().StringP("key", "k", "", "Path to SSH private key")
	deployCmd.Flags().BoolP("agent", "a", true, "Use SSH agent")
	deployCmd.Flags().DurationP("timeout", "t", 30*time.Second, "Connection timeout")
}

func runDeploy(cmd *cobra.Command, args []string) error {
	tarball := mustGetStringFlag(cmd, "tarball")
	doBuild := mustGetBoolFlag(cmd, "build")
	dryRun := mustGetBoolFlag(cmd, "dry-run")

	// If build requested, run `make build` locally first
	if doBuild {
		if dryRun {
			fmt.Println("[DRY RUN] Would run: make build (local)")
		} else {
			fmt.Println("Running: make build")
			cmdBuild := exec.Command("make", "build")
			cmdBuild.Stdout = os.Stdout
			cmdBuild.Stderr = os.Stderr
			if err := cmdBuild.Run(); err != nil {
				return fmt.Errorf("make build failed: %w", err)
			}
			// If tarball not provided, try to use default location
			if tarball == "" {
				// try to discover latest in ./dist
				if p, err := findLatestTarball("dist"); err == nil {
					tarball = p
					fmt.Printf("Using generated tarball: %s\n", tarball)
				}
			}
		}
	}
	// If tarball still empty, try to auto-discover the latest tarball in ./dist
	if tarball == "" {
		if p, err := findLatestTarball("dist"); err == nil {
			tarball = p
			fmt.Printf("Auto-discovered tarball: %s\n", tarball)
		}
	}
	if tarball == "" {
		return fmt.Errorf("--tarball is required")
	}
	if _, err := os.Stat(tarball); err != nil {
		return fmt.Errorf("tarball does not exist: %w", err)
	}

	serverRange, _ := cmd.Flags().GetString("server-range")
	if serverRange != "" {
		return processDeployForServerRange(cmd, serverRange, tarball)
	}

	if len(args) < 1 {
		return fmt.Errorf("hostname argument is required when --server-range is not used")
	}

	hostname := args[0]
	return deployToHost(cmd, hostname, tarball)
}

// findLatestTarball searches dir for *.tgz and *.tar.gz and returns the most recently
// modified matching file. Returns an error if none found.
func findLatestTarball(dir string) (string, error) {
	patterns := []string{"*.tgz", "*.tar.gz"}
	var latestPath string
	var latestMod time.Time

	for _, pat := range patterns {
		matches, err := filepath.Glob(filepath.Join(dir, pat))
		if err != nil {
			continue
		}
		for _, m := range matches {
			fi, err := os.Stat(m)
			if err != nil {
				continue
			}
			if fi.ModTime().After(latestMod) {
				latestMod = fi.ModTime()
				latestPath = m
			}
		}
	}

	if latestPath == "" {
		return "", fmt.Errorf("no tarball found in %s", dir)
	}
	return latestPath, nil
}

func processDeployForServerRange(cmd *cobra.Command, serverRange, tarball string) error {
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
		fmt.Printf("--- Deploying to: %s ---\n", hostname)
		if err := deployToHost(cmd, hostname, tarball); err != nil {
			fmt.Fprintf(os.Stderr, "Error deploying to %s: %v\n", hostname, err)
		}
		fmt.Println()
	}
	return nil
}

func deployToHost(cmd *cobra.Command, target, tarball string) error {
	dryRun := mustGetBoolFlag(cmd, "dry-run")
	sshClient, err := createSSHClient(cmd, target)
	if err != nil {
		return err
	}
	defer sshClient.Close()

	remoteDir := mustGetStringFlag(cmd, "remote-dir")
	strip, _ := cmd.Flags().GetInt("strip-components")
	startContainers := mustGetBoolFlag(cmd, "start-containers")

	// Copy tarball to remote /tmp
	remoteTmp := filepath.Join("/tmp", filepath.Base(tarball))
	if dryRun {
		fmt.Printf("[DRY RUN] Would upload %s -> %s:%s\n", tarball, target, remoteTmp)
	} else {
		fmt.Printf("Uploading %s -> %s:%s\n", tarball, target, remoteTmp)
		if err := sshClient.CopyFile(tarball, remoteTmp); err != nil {
			return fmt.Errorf("failed to upload tarball: %w", err)
		}
	}

	// Create target directory
	if dryRun {
		fmt.Printf("[DRY RUN] Would run on %s: mkdir -p %s\n", target, remoteDir)
	} else {
		if out, stderr, err := sshClient.ExecuteCommand(fmt.Sprintf("mkdir -p %s", remoteDir)); err != nil {
			return fmt.Errorf("failed to create remote dir: %w (stderr: %s, out: %s)", err, stderr, out)
		}
	}

	// Extract tarball
	extractCmd := fmt.Sprintf("tar -xzf %s -C %s --strip-components=%d", remoteTmp, remoteDir, strip)
	if dryRun {
		fmt.Printf("[DRY RUN] Would run on %s: %s\n", target, extractCmd)
	} else {
		fmt.Printf("Extracting on remote: %s\n", extractCmd)
		if out, stderr, err := sshClient.ExecuteCommand(extractCmd); err != nil {
			return fmt.Errorf("failed to extract on remote: %w (stderr: %s, out: %s)", err, stderr, out)
		}
	}

	// Try to locate binary and make executable
	remoteBinary := filepath.Join(remoteDir, "ciwg-cli")
	chmodCmd := fmt.Sprintf("if [ -f %s ]; then chmod +x %s; echo ok; fi", remoteBinary, remoteBinary)
	if dryRun {
		fmt.Printf("[DRY RUN] Would run on %s: %s\n", target, chmodCmd)
	} else {
		if out, stderr, err := sshClient.ExecuteCommand(chmodCmd); err != nil {
			// Non-fatal: continue
			fmt.Fprintf(os.Stderr, "warning: chmod on remote binary failed: %v (stderr: %s, out: %s)\n", err, stderr, out)
		}
	}

	// Create/replace symlink
	lnCmd := fmt.Sprintf("ln -sfn %s /usr/local/bin/ciwg-cli", remoteBinary)
	if dryRun {
		fmt.Printf("[DRY RUN] Would run on %s: %s\n", target, lnCmd)
	} else {
		if out, stderr, err := sshClient.ExecuteCommand(lnCmd); err != nil {
			return fmt.Errorf("failed to create symlink: %w (stderr: %s, out: %s)", err, stderr, out)
		}
	}

	// Optionally start containers if docker present
	if startContainers {
		startCmd := fmt.Sprintf(`if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then cd %q && docker compose up -d; elif command -v docker-compose >/dev/null 2>&1; then cd %q && docker-compose up -d; fi`, remoteDir, remoteDir)
		fmt.Printf("Starting containers on remote (if docker present)...\n")
		if dryRun {
			fmt.Printf("[DRY RUN] Would run on %s: %s\n", target, startCmd)
		} else {
			if out, stderr, err := sshClient.ExecuteCommand(startCmd); err != nil {
				fmt.Fprintf(os.Stderr, "warning: starting containers command failed: %v (stderr: %s, out: %s)\n", err, stderr, out)
			}
		}
	}

	// Clean up remote tmp
	cleanupCmd := fmt.Sprintf("rm -f %s", remoteTmp)
	if dryRun {
		fmt.Printf("[DRY RUN] Would run on %s: %s\n", target, cleanupCmd)
	} else {
		if _, _, err := sshClient.ExecuteCommand(cleanupCmd); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to remove remote tmp %s: %v\n", remoteTmp, err)
		}
	}

	fmt.Printf("Deployed to %s successfully\n", target)
	return nil
}
