package dnsbackup

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"ciwg-cli/internal/auth"
	dnsbackup "ciwg-cli/internal/dnsbackup"

	"github.com/spf13/cobra"
)

// siteMetadata represents a WordPress container discovered on a server.
type siteMetadata struct {
	Server  string
	Domain  string
	Website string
}

// zoneLookupResult stores the resolved Cloudflare zone and the sites that pointed to it.
type zoneLookupResult struct {
	ZoneName string
	Sites    []siteMetadata
}

func resolveZones(cmd *cobra.Command, args []string, client *dnsbackup.Client) ([]zoneLookupResult, error) {
	lookup := mustGetBoolFlag(cmd, "zone-lookup")
	if !lookup {
		if len(args) == 0 {
			return nil, errors.New("zone argument is required unless --zone-lookup is enabled")
		}
		zone := strings.TrimSpace(args[0])
		if zone == "" {
			return nil, errors.New("zone value cannot be empty")
		}
		return []zoneLookupResult{{ZoneName: zone}}, nil
	}

	serverRange := strings.TrimSpace(mustGetStringFlag(cmd, "server-range"))
	if serverRange != "" {
		if len(args) > 0 {
			return nil, errors.New("do not provide a zone argument when using --server-range with --zone-lookup")
		}
		return discoverZonesFromServers(cmd, client, serverRange)
	}

	if len(args) == 0 {
		return nil, errors.New("when --zone-lookup is enabled you must supply either --server-range or a single server hostname argument")
	}
	if len(args) > 1 {
		return nil, errors.New("only one server hostname may be provided when using --zone-lookup without --server-range")
	}
	host := strings.TrimSpace(args[0])
	if host == "" {
		return nil, errors.New("server hostname cannot be empty")
	}

	return collectZonesFromHosts(cmd, client, []string{host})
}

func discoverZonesFromServers(cmd *cobra.Command, client *dnsbackup.Client, pattern string) ([]zoneLookupResult, error) {
	hostPattern, start, end, exclusions, err := parseServerRange(pattern)
	if err != nil {
		return nil, err
	}

	var hostnames []string
	for i := start; i <= end; i++ {
		if exclusions[i] {
			continue
		}
		hostnames = append(hostnames, fmt.Sprintf(hostPattern, i))
	}
	if len(hostnames) == 0 {
		return nil, errors.New("server range did not yield any hosts")
	}

	return collectZonesFromHosts(cmd, client, hostnames)
}

func collectZonesFromHosts(cmd *cobra.Command, client *dnsbackup.Client, hostnames []string) ([]zoneLookupResult, error) {
	resolver := newHostZoneResolver(client)
	zoneMap := make(map[string]*zoneLookupResult)

	dedupHosts := make(map[string]struct{})
	for _, hostname := range hostnames {
		hostname = strings.TrimSpace(hostname)
		if hostname == "" {
			continue
		}
		if _, seen := dedupHosts[hostname]; seen {
			continue
		}
		dedupHosts[hostname] = struct{}{}

		sites, err := fetchSitesFromServer(cmd, hostname)
		if err != nil {
			return nil, fmt.Errorf("zone lookup failed on %s: %w", hostname, err)
		}
		for _, site := range sites {
			zoneName, _, err := resolver.resolveSite(site)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "Skipping site on %s: %v\n", site.Server, err)
				continue
			}
			entry, exists := zoneMap[zoneName]
			if !exists {
				entry = &zoneLookupResult{ZoneName: zoneName}
				zoneMap[zoneName] = entry
			}
			entry.Sites = append(entry.Sites, site)
		}
	}

	if len(zoneMap) == 0 {
		return nil, errors.New("no Cloudflare zones discovered from remote servers")
	}

	zoneNames := make([]string, 0, len(zoneMap))
	for zone := range zoneMap {
		zoneNames = append(zoneNames, zone)
	}
	sort.Strings(zoneNames)

	results := make([]zoneLookupResult, 0, len(zoneNames))
	for _, zone := range zoneNames {
		results = append(results, *zoneMap[zone])
	}

	fmt.Fprintf(cmd.ErrOrStderr(), "Zone lookup discovered %d zone(s)\n", len(results))
	return results, nil
}

func fetchSitesFromServer(cmd *cobra.Command, hostname string) ([]siteMetadata, error) {
	sshClient, err := createLookupSSHClient(cmd, hostname)
	if err != nil {
		return nil, err
	}
	defer sshClient.Close()

	listCmd := "docker ps --format '{{.Names}}' | grep '^wp_' || true"
	stdout, stderr, err := sshClient.ExecuteCommand(listCmd)
	if err != nil {
		return nil, fmt.Errorf("list containers: %w (stderr: %s)", err, strings.TrimSpace(stderr))
	}

	trimmed := strings.TrimSpace(stdout)
	if trimmed == "" {
		return nil, nil
	}

	containerNames := strings.Split(trimmed, "\n")
	var sites []siteMetadata

	for _, container := range containerNames {
		container = strings.TrimSpace(container)
		if container == "" {
			continue
		}

		domain := getContainerDomain(sshClient, container)
		website := getContainerWebsite(sshClient, container)
		if domain == "" && website == "" {
			continue
		}

		sites = append(sites, siteMetadata{
			Server:  hostname,
			Domain:  domain,
			Website: website,
		})
	}

	return sites, nil
}

func getContainerDomain(client *auth.SSHClient, container string) string {
	cmd := fmt.Sprintf(`docker inspect %s | jq -r '.[].Config.Labels."com.docker.compose.project.working_dir"'`, container)
	stdout, _, err := client.ExecuteCommand(cmd)
	if err != nil {
		return ""
	}
	workingDir := strings.TrimSpace(stdout)
	if workingDir == "" || workingDir == "null" {
		return ""
	}
	domain := filepath.Base(workingDir)
	return strings.ToLower(strings.TrimSpace(domain))
}

func getContainerWebsite(client *auth.SSHClient, container string) string {
	cmd := fmt.Sprintf(`docker inspect %s | jq -r '.[].Config.Env | map(select(test("^WP_HOME="))) | (.[0] // "")'`, container)
	stdout, _, err := client.ExecuteCommand(cmd)
	if err != nil {
		return ""
	}
	value := strings.TrimSpace(stdout)
	if value == "" || value == "null" {
		return ""
	}
	if strings.Contains(value, "=") {
		parts := strings.SplitN(value, "=", 2)
		value = parts[1]
	}
	return strings.TrimSpace(value)
}

type hostZoneResolver struct {
	client *dnsbackup.Client
	cache  map[string]string
}

func newHostZoneResolver(client *dnsbackup.Client) *hostZoneResolver {
	return &hostZoneResolver{
		client: client,
		cache:  make(map[string]string),
	}
}

func (r *hostZoneResolver) resolveSite(site siteMetadata) (string, string, error) {
	hosts := candidateHosts(site)
	if len(hosts) == 0 {
		return "", "", fmt.Errorf("no domain metadata found for site on %s", site.Server)
	}
	for _, host := range hosts {
		zone, err := r.resolveHost(host)
		if err == nil {
			return zone, host, nil
		}
	}
	return "", "", fmt.Errorf("no Cloudflare zone found for site on %s (candidates: %s)", site.Server, strings.Join(hosts, ", "))
}

func (r *hostZoneResolver) resolveHost(host string) (string, error) {
	normalized := normalizeHost(host)
	if normalized == "" {
		return "", fmt.Errorf("empty host")
	}
	if zone, ok := r.cache[normalized]; ok {
		if zone == "" {
			return "", fmt.Errorf("no Cloudflare zone matches host %s", normalized)
		}
		return zone, nil
	}
	zone, err := r.client.ResolveZoneName(normalized)
	if err != nil {
		r.cache[normalized] = ""
		return "", err
	}
	r.cache[normalized] = zone
	return zone, nil
}

func candidateHosts(site siteMetadata) []string {
	dedup := make(map[string]struct{})
	var hosts []string
	add := func(value string) {
		value = normalizeHost(value)
		if value == "" {
			return
		}
		if _, exists := dedup[value]; exists {
			return
		}
		dedup[value] = struct{}{}
		hosts = append(hosts, value)
	}
	if site.Website != "" {
		add(extractHostname(site.Website))
	}
	if site.Domain != "" {
		add(site.Domain)
	}
	return hosts
}

func extractHostname(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return normalizeHost(raw)
	}
	return normalizeHost(parsed.Hostname())
}

func normalizeHost(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.TrimPrefix(value, "www.")
	value = strings.Trim(value, ".")
	return value
}

func createLookupSSHClient(cmd *cobra.Command, hostname string) (*auth.SSHClient, error) {
	username := mustGetStringFlag(cmd, "lookup-user")
	if username == "" {
		username = currentUsername()
	}
	port := mustGetStringFlag(cmd, "lookup-port")
	if port == "" {
		port = "22"
	}
	keyPath := mustGetStringFlag(cmd, "lookup-key")
	useAgent := mustGetBoolFlag(cmd, "lookup-agent")
	timeout := mustGetDurationFlag(cmd, "lookup-timeout")
	disableDefaultKeys := mustGetBoolFlag(cmd, "lookup-disable-default-keys")
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	cfg := auth.SSHConfig{
		Hostname:           hostname,
		Username:           username,
		Port:               port,
		KeyPath:            keyPath,
		UseAgent:           useAgent,
		Timeout:            timeout,
		KeepAlive:          30 * time.Second,
		DisableDefaultKeys: disableDefaultKeys,
	}
	client, err := auth.NewSSHClient(cfg)
	if err == nil {
		return client, nil
	}
	if !isTooManyAuthFailures(err) {
		return nil, err
	}
	if !cfg.UseAgent {
		return nil, err
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "[%s] too many SSH authentication failures, retrying without SSH agent...\n", hostname)
	cfg.UseAgent = false
	return auth.NewSSHClient(cfg)
}

func isTooManyAuthFailures(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "too many authentication failures")
}
