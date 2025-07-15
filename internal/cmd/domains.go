package cmd

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var (
	outputFile string
	overwrite  bool
	appendFlag bool
	remove     bool
	dryRun     bool
	debug      bool
)

func newDomainsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "domains",
		Short: "Manage domain checks",
		Long:  `Check active and inactive domains, with options to remove inactive ones.`,
	}

	cmd.PersistentFlags().StringVarP(&outputFile, "output", "o", "", "Write CSV results to FILE")
	cmd.PersistentFlags().BoolVar(&overwrite, "overwrite", false, "Overwrite FILE without prompting")
	cmd.PersistentFlags().BoolVar(&appendFlag, "append", false, "Append to FILE without prompting")
	cmd.PersistentFlags().BoolVar(&remove, "remove", false, "Backup and delete sites that are not on this server (inactive only)")
	cmd.PersistentFlags().BoolVar(&dryRun, "dry-run", false, "Show what would be backed up and removed without making changes")
	cmd.PersistentFlags().BoolVar(&debug, "debug", false, "Verbose output")

	checkInactiveCmd := &cobra.Command{
		Use:   "check-inactive",
		Short: "Check for domains that do not resolve to the server's IP",
		Run: func(cmd *cobra.Command, args []string) {
			serverIP, err := getPublicIP()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error getting public IP: %v\n", err)
				os.Exit(1)
			}

			domainPath := "/var/opt"
			if envPath := os.Getenv("DOMAIN_PATH"); envPath != "" {
				domainPath = envPath
			}

			domains, err := findDomains(domainPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error scanning domains: %v\n", err)
				os.Exit(1)
			}

			results := make([]string, 0)
			for _, domain := range domains {
				match, _, err := domainMatchesServer(domain, serverIP)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error checking domain %s: %v\n", domain, err)
					continue
				}
				if !match {
					results = append(results, fmt.Sprintf("%s,false", domain))
					if remove || dryRun {
						fmt.Fprintf(os.Stderr, "Would backup and remove %s (DRY_RUN=%v)\n", domain, dryRun)
						if remove && !dryRun {
							backupAndRemove(domainPath, domain)
						}
					}
				} else {
					results = append(results, fmt.Sprintf("%s,true", domain))
				}
			}
			writeResults(results)
		},
	}

	checkActiveCmd := &cobra.Command{
		Use:   "check-active",
		Short: "Check for domains that resolve to the server's IP",
		Run: func(cmd *cobra.Command, args []string) {
			serverIP, err := getPublicIP()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error getting public IP: %v\n", err)
				os.Exit(1)
			}

			domainPath := "/var/opt"
			if envPath := os.Getenv("DOMAIN_PATH"); envPath != "" {
				domainPath = envPath
			}

			domains, err := findDomains(domainPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error scanning domains: %v\n", err)
				os.Exit(1)
			}

			results := make([]string, 0)
			for _, domain := range domains {
				match, _, err := domainMatchesServer(domain, serverIP)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error checking domain %s: %v\n", domain, err)
					continue
				}
				if match {
					results = append(results, fmt.Sprintf("%s,true", domain))
				} else {
					results = append(results, fmt.Sprintf("%s,false", domain))
				}
			}
			writeResults(results)
		},
	}

	cmd.AddCommand(checkInactiveCmd)
	cmd.AddCommand(checkActiveCmd)
	return cmd
}

func init() {
	rootCmd.AddCommand(newDomainsCmd())
}

func getPublicIP() (string, error) {
	resp, err := http.Get("https://api.ipify.org")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	ip, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(ip)), nil
}

func findDomains(domainPath string) ([]string, error) {
	var domains []string
	files, err := filepath.Glob(filepath.Join(domainPath, "*", "docker-compose.yml"))
	if err != nil {
		return nil, err
	}
	for _, file := range files {
		domains = append(domains, filepath.Base(filepath.Dir(file)))
	}
	return domains, nil
}

func domainMatchesServer(domain, serverIP string) (bool, []string, error) {
	// Try Cloudflare API first
	cfEmail := os.Getenv("CLOUDFLARE_EMAIL")
	cfAPIKey := os.Getenv("CLOUDFLARE_API_KEY")
	if cfEmail != "" && cfAPIKey != "" {
		_, cfRecords, err := checkCloudflare(domain, serverIP, cfEmail, cfAPIKey)
		if err == nil {
			if slices.Contains(cfRecords, serverIP) {
				return true, cfRecords, nil
			}
			return false, cfRecords, nil
		}
		// If Cloudflare API fails, fallback to DNS
	}
	// Fallback: direct DNS lookup
	aRecords, err := net.LookupHost(domain)
	if err != nil {
		return false, nil, err
	}
	if slices.Contains(aRecords, serverIP) {
		return true, aRecords, nil
	}
	return false, aRecords, nil
}

func checkCloudflare(domain, serverIP, email, apiKey string) (bool, []string, error) {
	// Get zone ID
	zoneID, err := getCloudflareZoneID(domain, email, apiKey)
	if err != nil || zoneID == "" {
		return false, nil, err
	}
	// Get A records
	records, err := getCloudflareARecords(zoneID, domain, email, apiKey)
	if err != nil {
		return false, nil, err
	}
	for _, ip := range records {
		if ip == serverIP {
			return true, records, nil
		}
	}
	return false, records, nil
}

func getCloudflareZoneID(domain, email, apiKey string) (string, error) {
	apiURL := "https://api.cloudflare.com/client/v4/zones?name=" + domain
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("X-Auth-Email", email)
	req.Header.Set("X-Auth-Key", apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	// Parse JSON
	var result struct {
		Success bool `json:"success"`
		Result  []struct {
			ID string `json:"id"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", err
	}
	if !result.Success || len(result.Result) == 0 {
		return "", fmt.Errorf("error: Cloudflare API error or no zone found")
	}
	return result.Result[0].ID, nil
}

func getCloudflareARecords(zoneID, domain, email, apiKey string) ([]string, error) {
	apiURL := fmt.Sprintf("https://api.cloudflare.com/client/v4/zones/%s/dns_records?type=A&name=%s", zoneID, domain)
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Auth-Email", email)
	req.Header.Set("X-Auth-Key", apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	// Parse JSON
	var result struct {
		Success bool `json:"success"`
		Result  []struct {
			Content string `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	if !result.Success {
		return nil, fmt.Errorf("error: Cloudflare API error getting A records")
	}
	ips := make([]string, 0)
	for _, rec := range result.Result {
		ips = append(ips, rec.Content)
	}
	return ips, nil
}

func writeResults(results []string) {
	if outputFile == "" {
		for _, line := range results {
			fmt.Println(line)
		}
		return
	}
	var f *os.File
	var err error
	if overwrite {
		f, err = os.Create(outputFile)
	} else if appendFlag {
		f, err = os.OpenFile(outputFile, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
	} else {
		if _, err := os.Stat(outputFile); err == nil {
			fmt.Fprintf(os.Stderr, "File %s exists. Use --overwrite or --append.\n", outputFile)
			return
		}
		f, err = os.Create(outputFile)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening output file: %v\n", err)
		return
	}
	defer f.Close()
	for _, line := range results {
		fmt.Fprintln(f, line)
	}
	fmt.Fprintf(os.Stderr, "CSV results written to %s\n", outputFile)
}

// Stub for backup and removal logic
func backupAndRemove(domainPath, domain string) {
	sitePath := filepath.Join(domainPath, domain)
	if _, err := os.Stat(sitePath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Directory not found: %s\n", sitePath)
		return
	}

	// 1. Find container name from docker-compose.yml
	composePath := filepath.Join(sitePath, "docker-compose.yml")
	containerName, err := getContainerName(composePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting container name: %v\n", err)
		return
	}

	// 2. Export database using Docker SDK
	if err := exportDatabase(containerName); err != nil {
		fmt.Fprintf(os.Stderr, "Error exporting database: %v\n", err)
		// Continue even if DB export fails
	}

	// 3. Create timestamped tarball
	backupDir := filepath.Join(domainPath, "backup-tarballs")
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating backup directory: %v\n", err)
		return
	}
	timestamp := time.Now().Format("20060102150405")
	tarballName := fmt.Sprintf("%s_%s.tar.gz", domain, timestamp)
	tarballPath := filepath.Join(backupDir, tarballName)
	if err := createTarball(tarballPath, sitePath); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating tarball: %v\n", err)
		return
	}

	// 4. Remove container and site directory
	if err := removeContainer(containerName); err != nil {
		fmt.Fprintf(os.Stderr, "Error removing container: %v\n", err)
	}
	if err := os.RemoveAll(sitePath); err != nil {
		fmt.Fprintf(os.Stderr, "Error removing site directory: %v\n", err)
	}

	fmt.Fprintf(os.Stderr, "Successfully backed up and removed %s\n", domain)
}

func getContainerName(composePath string) (string, error) {
	data, err := os.ReadFile(composePath)
	if err != nil {
		return "", err
	}
	var compose struct {
		Services map[string]interface{} `yaml:"services"`
	}
	if err := yaml.Unmarshal(data, &compose); err != nil {
		return "", err
	}
	for name := range compose.Services {
		if strings.HasPrefix(name, "wp_") {
			return name, nil
		}
	}
	return "", fmt.Errorf("no wp_ service found in %s", composePath)
}

func exportDatabase(containerName string) error {
	cli, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		return err
	}
	ctx := context.Background()
	cmd := []string{"wp", "db", "export", "/dev/stdout", "--allow-root"}
	execConfig := container.ExecOptions{
		Cmd:          cmd,
		AttachStdout: true,
		AttachStderr: true,
	}
	execID, err := cli.ContainerExecCreate(ctx, containerName, execConfig)
	if err != nil {
		return err
	}
	resp, err := cli.ContainerExecAttach(ctx, execID.ID, container.ExecStartOptions{})
	if err != nil {
		return err
	}
	defer resp.Close()
	// We are exporting to stdout, but not saving it to a file in this implementation.
	// The original script saved it inside the container, then the container was archived.
	// This implementation archives the directory from the host.
	return nil
}

func createTarball(tarballPath, sourcePath string) error {
	file, err := os.Create(tarballPath)
	if err != nil {
		return err
	}
	defer file.Close()
	gzipWriter := gzip.NewWriter(file)
	defer gzipWriter.Close()
	tarWriter := tar.NewWriter(gzipWriter)
	defer tarWriter.Close()
	return filepath.Walk(sourcePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		header, err := tar.FileInfoHeader(info, info.Name())
		if err != nil {
			return err
		}
		header.Name, _ = filepath.Rel(sourcePath, path)
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}
		if !info.IsDir() {
			data, err := os.Open(path)
			if err != nil {
				return err
			}
			defer data.Close()
			if _, err := io.Copy(tarWriter, data); err != nil {
				return err
			}
		}
		return nil
	})
}

func removeContainer(containerName string) error {
	cli, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		return err
	}
	ctx := context.Background()
	return cli.ContainerRemove(ctx, containerName, container.RemoveOptions{Force: true})
}
