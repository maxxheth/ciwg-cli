package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

var (
	pluginsServerRange    string
	pluginsUpdatesOnly    bool
	pluginsPatch          bool
	pluginsDryRun         bool
	pluginsOutputFormat   string
	pluginsInclude        []string
	pluginsExclude        []string
	pluginsCheckIfFailed  bool
	pluginsUpdateInsecure bool
	pluginsInsecure       bool
)

var pluginsCmd = &cobra.Command{
	Use:   "plugins",
	Short: "Manage WordPress plugins across servers",
	Long:  `List and update WordPress plugins across multiple servers using WP-CLI.`,
}

var pluginsCheckCmd = &cobra.Command{
	Use:   "check [hostname]",
	Short: "Check plugin status and list available updates",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runPluginsCheck,
}

var pluginsUpdateCmd = &cobra.Command{
	Use:   "update [hostname] [plugins...]",
	Short: "Update plugins with available updates",
	Long: `Update plugins on a specific server or a range of servers.

If --server-range is provided, all arguments are treated as plugin names to update.
If --server-range is NOT provided, the first argument is the hostname, and subsequent arguments are plugin names.

Examples:
  # Update all plugins on a specific server
  ciwg plugins update wp0.ciwgserver.com

  # Update specific plugins on a specific server
  ciwg plugins update wp0.ciwgserver.com elementor elementor-pro

  # Update all plugins on a range of servers
  ciwg plugins update --server-range="wp%d.ciwgserver.com:0-5"

  # Update specific plugins on a range of servers
  ciwg plugins update elementor --server-range="wp%d.ciwgserver.com:0-5"`,
	RunE: runPluginsUpdate,
}

func init() {
	rootCmd.AddCommand(pluginsCmd)
	pluginsCmd.AddCommand(pluginsCheckCmd)
	pluginsCmd.AddCommand(pluginsUpdateCmd)

	// Check flags
	pluginsCheckCmd.Flags().StringVar(&pluginsServerRange, "server-range", "", "Server range pattern (e.g., 'wp%d.ciwgserver.com:0-41')")
	pluginsCheckCmd.Flags().BoolVar(&pluginsUpdatesOnly, "updates-only", false, "Show only plugins with available updates")
	pluginsCheckCmd.Flags().StringVarP(&pluginsOutputFormat, "output", "o", "table", "Output format (table, json, csv)")
	pluginsCheckCmd.Flags().StringSliceVar(&pluginsInclude, "include", []string{}, "Comma-separated list of containers to include")
	pluginsCheckCmd.Flags().StringSliceVar(&pluginsExclude, "exclude", []string{}, "Comma-separated list of containers to exclude")
	pluginsCheckCmd.Flags().BoolVar(&pluginsCheckIfFailed, "check-if-failed", false, "Check Stream logs for failed plugin updates")
	pluginsCheckCmd.Flags().BoolVar(&pluginsUpdateInsecure, "update-insecure", false, "Update failed plugins with --insecure flag (implies --check-if-failed)")
	addSSHFlags(pluginsCheckCmd)

	// Update flags
	pluginsUpdateCmd.Flags().StringVar(&pluginsServerRange, "server-range", "", "Server range pattern (e.g., 'wp%d.ciwgserver.com:0-41')")
	pluginsUpdateCmd.Flags().BoolVar(&pluginsDryRun, "dry-run", false, "Simulate updates without applying them")
	pluginsUpdateCmd.Flags().StringSliceVar(&pluginsInclude, "include", []string{}, "Comma-separated list of containers to include")
	pluginsUpdateCmd.Flags().StringSliceVar(&pluginsExclude, "exclude", []string{}, "Comma-separated list of containers to exclude")
	pluginsUpdateCmd.Flags().BoolVar(&pluginsInsecure, "insecure", false, "Use --insecure flag for wp plugin update (skip SSL verification)")
	addSSHFlags(pluginsUpdateCmd)
}

type PluginUpdateStatus string

func (s *PluginUpdateStatus) UnmarshalJSON(data []byte) error {
	var v interface{}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	switch value := v.(type) {
	case string:
		*s = PluginUpdateStatus(value)
	case bool:
		if value {
			*s = "available"
		} else {
			*s = "none"
		}
	default:
		*s = "none"
	}
	return nil
}

type Plugin struct {
	Name          string             `json:"name"`
	Status        string             `json:"status"`
	Update        PluginUpdateStatus `json:"update"`
	Version       string             `json:"version"`
	UpdateVersion string             `json:"update_version"`
	AutoUpdate    string             `json:"auto_update"`
	Container     string             `json:"-"`
	Server        string             `json:"-"`
}

type FailedPlugin struct {
	Name      string
	Container string
	Server    string
}

func runPluginsCheck(cmd *cobra.Command, args []string) error {
	// If --update-insecure is set, enable --check-if-failed and insecure mode
	if pluginsUpdateInsecure {
		pluginsCheckIfFailed = true
		pluginsInsecure = true
	}

	var hostnames []string

	if len(args) > 0 {
		hostnames = []string{args[0]}
	} else if pluginsServerRange != "" {
		pattern, start, end, exclusions, err := parseServerRange(pluginsServerRange)
		if err != nil {
			return err
		}
		for i := start; i <= end; i++ {
			if !exclusions[i] {
				hostnames = append(hostnames, fmt.Sprintf(pattern, i))
			}
		}
	} else {
		return fmt.Errorf("hostname argument or --server-range flag is required")
	}

	var allPlugins []Plugin
	var failedPlugins []FailedPlugin
	var mu sync.Mutex
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, 10) // Limit concurrency

	for _, hostname := range hostnames {
		wg.Add(1)
		go func(host string) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			plugins, err := getPluginsOnServer(cmd, host)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error on %s: %v\n", host, err)
				return
			}

			mu.Lock()
			allPlugins = append(allPlugins, plugins...)
			mu.Unlock()

			// Check for failed updates if requested
			if pluginsCheckIfFailed {
				failed, err := getFailedPluginsOnServer(cmd, host)
				if err != nil {
					if verbose, _ := cmd.Flags().GetBool("verbose"); verbose {
						fmt.Fprintf(os.Stderr, "Error checking failed plugins on %s: %v\n", host, err)
					}
					return
				}
				mu.Lock()
				failedPlugins = append(failedPlugins, failed...)
				mu.Unlock()
			}
		}(hostname)
	}

	wg.Wait()

	// If --update-insecure, attempt to fix failed plugins
	if pluginsUpdateInsecure && len(failedPlugins) > 0 {
		fmt.Fprintf(os.Stderr, "\nAttempting to update %d failed plugins with --insecure flag...\n\n", len(failedPlugins))

		for _, fp := range failedPlugins {
			wg.Add(1)
			go func(failed FailedPlugin) {
				defer wg.Done()
				semaphore <- struct{}{}
				defer func() { <-semaphore }()

				if err := updateFailedPlugin(cmd, failed); err != nil {
					fmt.Fprintf(os.Stderr, "Error updating %s on %s/%s: %v\n",
						failed.Name, failed.Server, failed.Container, err)
				}
			}(fp)
		}
		wg.Wait()
		return nil
	}

	// Display failed plugins if check-if-failed is set
	if pluginsCheckIfFailed && len(failedPlugins) > 0 {
		fmt.Fprintf(os.Stderr, "\n=== Failed Plugin Updates ===\n")
		fw := tabwriter.NewWriter(os.Stderr, 0, 0, 3, ' ', 0)
		fmt.Fprintln(fw, "SERVER\tCONTAINER\tPLUGIN")
		for _, fp := range failedPlugins {
			fmt.Fprintf(fw, "%s\t%s\t%s\n", fp.Server, fp.Container, fp.Name)
		}
		fw.Flush()
		fmt.Fprintf(os.Stderr, "\nTotal failed updates: %d\n", len(failedPlugins))
		fmt.Fprintf(os.Stderr, "To retry with --insecure flag, run with --update-insecure\n\n")
	}

	// Filter plugins
	filteredPlugins := make([]Plugin, 0)
	for _, p := range allPlugins {
		if pluginsUpdatesOnly && p.Update != "available" {
			continue
		}
		filteredPlugins = append(filteredPlugins, p)
	}

	switch strings.ToLower(pluginsOutputFormat) {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(filteredPlugins)
	case "csv":
		fmt.Println("SERVER,CONTAINER,PLUGIN,STATUS,VERSION,UPDATE,NEW_VERSION")
		for _, p := range filteredPlugins {
			fmt.Printf("%s,%s,%s,%s,%s,%s,%s\n",
				p.Server, p.Container, p.Name, p.Status, p.Version, p.Update, p.UpdateVersion)
		}
	default:
		// Filter and display
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		fmt.Fprintln(w, "SERVER\tCONTAINER\tPLUGIN\tSTATUS\tVERSION\tUPDATE\tNEW_VERSION")

		if len(filteredPlugins) == 0 {
			w.Flush()
			fmt.Println("No plugins found matching criteria.")
			return nil
		}

		for _, p := range filteredPlugins {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				p.Server, p.Container, p.Name, p.Status, p.Version, p.Update, p.UpdateVersion)
		}
		w.Flush()
	}

	return nil
}

func runPluginsUpdate(cmd *cobra.Command, args []string) error {
	var hostnames []string
	var targetPlugins []string

	if pluginsServerRange != "" {
		// If server-range is provided, all args are plugins
		targetPlugins = args
		pattern, start, end, exclusions, err := parseServerRange(pluginsServerRange)
		if err != nil {
			return err
		}
		for i := start; i <= end; i++ {
			if !exclusions[i] {
				hostnames = append(hostnames, fmt.Sprintf(pattern, i))
			}
		}
	} else {
		// If no server-range, first arg is hostname, rest are plugins
		if len(args) > 0 {
			hostnames = []string{args[0]}
			targetPlugins = args[1:]
		} else {
			return fmt.Errorf("hostname argument or --server-range flag is required")
		}
	}

	var wg sync.WaitGroup
	semaphore := make(chan struct{}, 10)

	for _, hostname := range hostnames {
		wg.Add(1)
		go func(host string) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			if err := updatePluginsOnServer(cmd, host, targetPlugins); err != nil {
				fmt.Fprintf(os.Stderr, "Error updating on %s: %v\n", host, err)
			}
		}(hostname)
	}

	wg.Wait()
	return nil
}

func getPluginsOnServer(cmd *cobra.Command, hostname string) ([]Plugin, error) {
	sshClient, err := createSSHClient(cmd, hostname)
	if err != nil {
		return nil, err
	}
	defer sshClient.Close()

	// Find WP containers
	stdout, _, err := sshClient.ExecuteCommand("docker ps --format '{{.Names}}' | grep '^wp_'")
	if err != nil {
		return nil, nil // No containers or error
	}

	containers := strings.Split(strings.TrimSpace(stdout), "\n")
	var serverPlugins []Plugin

	for _, container := range containers {
		if container == "" {
			continue
		}

		if !isContainerAllowed(container) {
			continue
		}

		// Get plugins
		// Use sh -c to ensure PATH is resolved correctly and handle potential execution issues
		// We also check if wp exists to avoid errors on non-WP containers
		jsonCmd := fmt.Sprintf("docker exec -u 0 %s sh -c 'command -v wp >/dev/null 2>&1 && wp plugin list --format=json --allow-root || true'", container)
		out, stderr, err := sshClient.ExecuteCommand(jsonCmd)
		if err != nil {
			// Only log errors in verbose mode to avoid cluttering output for non-WP containers
			if verbose, _ := cmd.Flags().GetBool("verbose"); verbose {
				fmt.Fprintf(os.Stderr, "Failed to list plugins on %s/%s: %v\nStderr: %s\n", hostname, container, err, stderr)
			}
			continue
		}

		// If output is empty, it means wp command failed or returned nothing (e.g. not a WP site)
		if strings.TrimSpace(out) == "" {
			if verbose, _ := cmd.Flags().GetBool("verbose"); verbose {
				fmt.Fprintf(os.Stderr, "[%s] No output from wp plugin list (likely not a WP site)\n", container)
			}
			continue
		}

		var plugins []Plugin
		if err := json.Unmarshal([]byte(out), &plugins); err != nil {
			if verbose, _ := cmd.Flags().GetBool("verbose"); verbose {
				fmt.Fprintf(os.Stderr, "[%s] Failed to parse JSON output: %v\nOutput was: %s\n", container, err, out)
			}
			continue
		}

		// Log success in verbose mode
		if verbose, _ := cmd.Flags().GetBool("verbose"); verbose {
			updatesCount := 0
			for _, p := range plugins {
				if p.Update == "available" {
					updatesCount++
				}
			}
			if updatesCount == 0 {
				fmt.Fprintf(os.Stderr, "[%s] Successfully scanned %d plugins on %s (All up to date)\n", container, len(plugins), hostname)
			} else {
				fmt.Fprintf(os.Stderr, "[%s] Successfully scanned %d plugins on %s (%d updates available)\n", container, len(plugins), hostname, updatesCount)
			}
		}

		for i := range plugins {
			plugins[i].Container = container
			plugins[i].Server = hostname
			serverPlugins = append(serverPlugins, plugins[i])
		}
	}

	return serverPlugins, nil
}

func getFailedPluginsOnServer(cmd *cobra.Command, hostname string) ([]FailedPlugin, error) {
	sshClient, err := createSSHClient(cmd, hostname)
	if err != nil {
		return nil, err
	}
	defer sshClient.Close()

	// Find WP containers
	stdout, _, err := sshClient.ExecuteCommand("docker ps --format '{{.Names}}' | grep '^wp_'")
	if err != nil {
		return nil, nil
	}

	containers := strings.Split(strings.TrimSpace(stdout), "\n")
	var failedPlugins []FailedPlugin

	for _, container := range containers {
		if container == "" {
			continue
		}

		if !isContainerAllowed(container) {
			continue
		}

		// Check Stream logs for failed plugin updates
		// Pattern: Plugin '<plugin-slug>' update failed
		grepCmd := fmt.Sprintf("docker exec %s wp stream list --format=csv --fields=summary --allow-root 2>/dev/null | grep -oP \"Plugin '\\K[^']+(?=' update failed)\" || true", container)
		out, _, err := sshClient.ExecuteCommand(grepCmd)
		if err != nil || strings.TrimSpace(out) == "" {
			continue
		}

		pluginNames := strings.Split(strings.TrimSpace(out), "\n")
		for _, name := range pluginNames {
			name = strings.TrimSpace(name)
			if name != "" {
				failedPlugins = append(failedPlugins, FailedPlugin{
					Name:      name,
					Container: container,
					Server:    hostname,
				})
			}
		}
	}

	return failedPlugins, nil
}

func updateFailedPlugin(cmd *cobra.Command, failed FailedPlugin) error {
	sshClient, err := createSSHClient(cmd, failed.Server)
	if err != nil {
		return err
	}
	defer sshClient.Close()

	fmt.Printf("[%s/%s] Updating %s with --insecure flag...\n", failed.Server, failed.Container, failed.Name)

	if pluginsDryRun {
		fmt.Printf("[%s/%s] Dry run: Would update %s with --insecure\n", failed.Server, failed.Container, failed.Name)
		return nil
	}

	updateCmd := fmt.Sprintf("docker exec %s wp plugin update %s --allow-root --insecure", failed.Container, failed.Name)
	upOut, upErr, err := sshClient.ExecuteCommand(updateCmd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[%s/%s] Update failed: %v\n%s\n", failed.Server, failed.Container, err, upErr)
		return err
	}

	fmt.Printf("[%s/%s] Update result:\n%s\n", failed.Server, failed.Container, upOut)
	return nil
}

func updatePluginsOnServer(cmd *cobra.Command, hostname string, targetPlugins []string) error {
	sshClient, err := createSSHClient(cmd, hostname)
	if err != nil {
		return err
	}
	defer sshClient.Close()

	// Find WP containers
	stdout, _, err := sshClient.ExecuteCommand("docker ps --format '{{.Names}}' | grep '^wp_'")
	if err != nil {
		return nil
	}

	containers := strings.Split(strings.TrimSpace(stdout), "\n")

	for _, container := range containers {
		if container == "" {
			continue
		}

		if !isContainerAllowed(container) {
			continue
		}

		// Check for updates first
		checkCmd := fmt.Sprintf("docker exec -u 0 %s sh -c 'command -v wp >/dev/null 2>&1 && wp plugin list --format=json --allow-root || true'", container)
		out, _, err := sshClient.ExecuteCommand(checkCmd)
		if err != nil {
			continue
		}

		var plugins []Plugin
		if err := json.Unmarshal([]byte(out), &plugins); err != nil {
			continue
		}

		updatesAvailable := false
		for _, p := range plugins {
			if p.Update == "available" {
				updatesAvailable = true
				// Only print if we are updating all, or if this plugin is in our target list
				shouldPrint := len(targetPlugins) == 0
				if !shouldPrint {
					for _, t := range targetPlugins {
						if t == p.Name {
							shouldPrint = true
							break
						}
					}
				}
				if shouldPrint {
					fmt.Printf("[%s] Update available for %s: %s -> %s\n", container, p.Name, p.Version, p.UpdateVersion)
				}
			}
		}

		if len(targetPlugins) == 0 && !updatesAvailable {
			fmt.Printf("[%s] All plugins are up to date.\n", container)
			continue
		}

		if pluginsDryRun {
			fmt.Printf("[%s] Dry run: Skipping update\n", container)
			continue
		}

		// Perform update
		fmt.Printf("[%s] Updating plugins...\n", container)

		updateArgs := "--all"
		if len(targetPlugins) > 0 {
			updateArgs = strings.Join(targetPlugins, " ")
		}

		insecureFlag := ""
		if pluginsInsecure {
			insecureFlag = " --insecure"
		}

		updateCmd := fmt.Sprintf("docker exec %s wp plugin update %s --allow-root%s", container, updateArgs, insecureFlag)
		upOut, upErr, err := sshClient.ExecuteCommand(updateCmd)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[%s] Update failed: %v\n%s\n", container, err, upErr)
		} else {
			fmt.Printf("[%s] Update result:\n%s\n", container, upOut)
		}
	}

	return nil
}

func isContainerAllowed(name string) bool {
	// Check exclude first
	for _, ex := range pluginsExclude {
		if ex == name {
			return false
		}
	}

	// If include list is empty, allow all (unless excluded)
	if len(pluginsInclude) == 0 {
		return true
	}

	// Check include
	for _, inc := range pluginsInclude {
		if inc == name {
			return true
		}
	}

	return false
}
