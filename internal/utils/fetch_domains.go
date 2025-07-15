package utils

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

// FetchDomainsRawJSON fetches the raw JSON response from the remote API using the token from the environment
func FetchDomainsRawJSON() ([]byte, error) {
	token := os.Getenv("CIWG_DOMAIN_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("CIWG_DOMAIN_TOKEN not set in environment")
	}
	url := fmt.Sprintf("https://digital-ocean.ciwebgroup.com/domains.json?token=%s", token)
	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}
	return body, nil
}

type DomainInfo struct {
	Domain  string `json:"domain"`
	Website string `json:"website"`
	Server  string `json:"server"`
	IP      string `json:"ip"`
}

// FetchDomainsFromAPI fetches domain info from the remote API using the token from the environment
func FetchDomainsFromAPI() ([]DomainInfo, error) {
	token := os.Getenv("CIWG_DOMAIN_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("CIWG_DOMAIN_TOKEN not set in environment")
	}
	url := fmt.Sprintf("https://digital-ocean.ciwebgroup.com/domains.json?token=%s", token)
	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned status %d", resp.StatusCode)
	}
	var domains []DomainInfo
	if err := json.NewDecoder(resp.Body).Decode(&domains); err != nil {
		return nil, fmt.Errorf("failed to decode JSON: %w", err)
	}
	return domains, nil
}
