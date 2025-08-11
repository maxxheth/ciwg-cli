package wpscan

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	baseURL         = "https://wpscan.com/api/v3"
	reserveRequests = 5
	maxRetries      = 3
)

type APIClient struct {
	httpClient   *http.Client
	tokens       []string
	currentToken int
	requestsLeft int
	mu           sync.Mutex
}

type APIClientConfig struct {
	CSVFile   string
	CSVColumn string
}

// Update APIStatus to handle null values properly
type APIStatus struct {
	Plan          string      `json:"plan"`
	RequestsLeft  interface{} `json:"requests_left"`  // Can be int or null
	RequestsLimit interface{} `json:"requests_limit"` // Can be int or null
}

// Helper method to safely extract int from interface{} (handles null)
func (s *APIStatus) GetRequestsLeft() int {
	switch v := s.RequestsLeft.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case nil:
		return -1 // Use -1 to indicate unknown/unlimited
	default:
		return -1
	}
}

func (s *APIStatus) GetRequestsLimit() int {
	switch v := s.RequestsLimit.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case nil:
		return -1 // Use -1 to indicate unknown/unlimited
	default:
		return -1
	}
}

type PluginVulnInfo struct {
	Slug            string          `json:"slug"`
	LatestVersion   string          `json:"latest_version"`
	LastUpdated     string          `json:"last_updated"`
	Popular         bool            `json:"popular"`
	Vulnerabilities []Vulnerability `json:"vulnerabilities"`
}

type ThemeVulnInfo struct {
	Slug            string          `json:"slug"`
	LatestVersion   string          `json:"latest_version"`
	LastUpdated     string          `json:"last_updated"`
	Popular         bool            `json:"popular"`
	Vulnerabilities []Vulnerability `json:"vulnerabilities"`
}

type Vulnerability struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	CreatedAt  string   `json:"created_at"`
	UpdatedAt  string   `json:"updated_at"`
	CVE        []string `json:"cve,omitempty"`
	FixedIn    string   `json:"fixed_in,omitempty"`
	References struct {
		URL   []string `json:"url,omitempty"`
		WPVDB []string `json:"wpvdb,omitempty"`
	} `json:"references"`
}

// NewAPIClient creates an API client using environment variables
func NewAPIClient() *APIClient {
	config := APIClientConfig{}
	client, err := NewAPIClientWithConfig(config)
	if err != nil {
		log.Fatal(err)
	}
	return client
}

// NewAPIClientWithConfig creates an API client with specified configuration
func NewAPIClientWithConfig(config APIClientConfig) (*APIClient, error) {
	var tokens []string
	var err error

	// Try to load from CSV first if specified
	if config.CSVFile != "" {
		tokens, err = loadTokensFromCSV(config.CSVFile, config.CSVColumn)
		if err != nil {
			return nil, fmt.Errorf("failed to load API tokens from CSV: %w", err)
		}
		log.Printf("Loaded %d API tokens from CSV file: %s", len(tokens), config.CSVFile)
	}

	// If no tokens from CSV, try environment variables
	if len(tokens) == 0 {
		tokens = loadTokensFromEnv()
	}

	if len(tokens) == 0 {
		return nil, fmt.Errorf("no API tokens found. Provide tokens via CSV file, API_TOKEN_1/API_TOKEN_2/etc., or WPSCAN_API_TOKEN environment variables")
	}

	log.Printf("Initialized WPScan API client with %d tokens", len(tokens))

	return &APIClient{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		tokens:       tokens,
		currentToken: 0,
		requestsLeft: -1, // Initialize as unknown
	}, nil
}

// loadTokensFromCSV loads API tokens from a CSV file
func loadTokensFromCSV(filename, columnName string) ([]string, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to open CSV file: %w", err)
	}
	defer file.Close()

	reader := csv.NewReader(file)
	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("failed to read CSV: %w", err)
	}

	if len(records) == 0 {
		return nil, fmt.Errorf("CSV file is empty")
	}

	// Find the column index
	header := records[0]
	columnIndex := -1
	for i, col := range header {
		if strings.EqualFold(strings.TrimSpace(col), columnName) {
			columnIndex = i
			break
		}
	}

	if columnIndex == -1 {
		return nil, fmt.Errorf("column '%s' not found in CSV. Available columns: %v", columnName, header)
	}

	// Extract tokens from the specified column
	var tokens []string
	seenTokens := make(map[string]bool) // Deduplicate tokens

	for i, record := range records[1:] {
		if columnIndex >= len(record) {
			log.Printf("Warning: Row %d doesn't have enough columns, skipping", i+2)
			continue
		}

		token := strings.TrimSpace(record[columnIndex])
		if token != "" && !seenTokens[token] {
			tokens = append(tokens, token)
			seenTokens[token] = true
		}
	}

	if len(tokens) == 0 {
		return nil, fmt.Errorf("no valid API tokens found in column '%s'", columnName)
	}

	return tokens, nil
}

// loadTokensFromEnv loads API tokens from environment variables
func loadTokensFromEnv() []string {
	var tokens []string

	// Load numbered tokens (API_TOKEN_1, API_TOKEN_2, etc.)
	for i := 1; ; i++ {
		token := os.Getenv(fmt.Sprintf("API_TOKEN_%d", i))
		if token == "" {
			break
		}
		tokens = append(tokens, token)
	}

	// If no numbered tokens, try the default token
	if len(tokens) == 0 {
		if token := os.Getenv("WPSCAN_API_TOKEN"); token != "" {
			tokens = append(tokens, token)
		}
	}

	return tokens
}

func (c *APIClient) checkAndRotateToken(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	status, err := c.getStatus(ctx)
	if err != nil {
		return err
	}

	requestsLeft := status.GetRequestsLeft()
	requestsLimit := status.GetRequestsLimit()
	c.requestsLeft = requestsLeft

	// Log token status with proper null handling
	if requestsLeft == -1 {
		log.Printf("Token %d (%s plan): requests remaining unknown/unlimited", c.currentToken+1, status.Plan)
	} else {
		if requestsLimit == -1 {
			log.Printf("Token %d (%s plan): %d requests remaining (limit unknown)", c.currentToken+1, status.Plan, requestsLeft)
		} else {
			log.Printf("Token %d (%s plan): %d/%d requests remaining", c.currentToken+1, status.Plan, requestsLeft, requestsLimit)
		}
	}

	// Only rotate if we have a known requests left value and it's at or below reserve
	// If requests_left is null (unknown), we don't rotate - let the API tell us when we're rate limited
	if requestsLeft > 0 && requestsLeft <= reserveRequests {
		if c.currentToken < len(c.tokens)-1 {
			c.currentToken++
			log.Printf("Rotating to token %d (reserve threshold reached)", c.currentToken+1)

			// Check new token status
			status, err := c.getStatus(ctx)
			if err != nil {
				return err
			}
			c.requestsLeft = status.GetRequestsLeft()

			newRequestsLeft := status.GetRequestsLeft()
			if newRequestsLeft == -1 {
				log.Printf("New token %d (%s plan): requests remaining unknown/unlimited", c.currentToken+1, status.Plan)
			} else {
				log.Printf("New token %d (%s plan): %d requests remaining", c.currentToken+1, status.Plan, newRequestsLeft)
			}
		} else {
			// Only fail if we know we're at the limit - if it's unknown, let the API decide
			return fmt.Errorf("all tokens at reserve limit (%d requests remaining on last token)", requestsLeft)
		}
	}

	return nil
}

func (c *APIClient) getStatus(ctx context.Context) (*APIStatus, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", baseURL+"/status", nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", fmt.Sprintf("Token token=%s", c.tokens[c.currentToken]))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("status check failed: %d - %s", resp.StatusCode, string(body))
	}

	var status APIStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return nil, err
	}

	return &status, nil
}

func (c *APIClient) makeRequest(ctx context.Context, endpoint string) ([]byte, error) {
	// Check token status before making request
	if err := c.checkAndRotateToken(ctx); err != nil {
		return nil, err
	}

	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * time.Second)
		}

		req, err := http.NewRequestWithContext(ctx, "GET", baseURL+endpoint, nil)
		if err != nil {
			return nil, err
		}

		req.Header.Set("Authorization", fmt.Sprintf("Token token=%s", c.tokens[c.currentToken]))

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			lastErr = err
			continue
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			// Token exhausted, try to rotate
			c.mu.Lock()
			if c.currentToken < len(c.tokens)-1 {
				c.currentToken++
				log.Printf("Rate limited (429), rotating to token %d", c.currentToken+1)
				c.mu.Unlock()
				continue
			}
			c.mu.Unlock()
			return nil, fmt.Errorf("rate limited and all tokens exhausted")
		}

		if resp.StatusCode != http.StatusOK {
			if resp.StatusCode == http.StatusNotFound {
				// Asset not in database
				return nil, nil
			}
			lastErr = fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
			continue
		}

		return body, nil
	}

	return nil, fmt.Errorf("request failed after %d attempts: %w", maxRetries, lastErr)
}

func (c *APIClient) GetPluginVulnerabilities(ctx context.Context, slug string) (*PluginVulnInfo, error) {
	data, err := c.makeRequest(ctx, fmt.Sprintf("/plugins/%s", slug))
	if err != nil {
		return nil, err
	}

	if data == nil {
		// Plugin not found in database
		return &PluginVulnInfo{
			Slug:            slug,
			Vulnerabilities: []Vulnerability{},
		}, nil
	}

	var info PluginVulnInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, err
	}

	return &info, nil
}

func (c *APIClient) GetThemeVulnerabilities(ctx context.Context, slug string) (*ThemeVulnInfo, error) {
	data, err := c.makeRequest(ctx, fmt.Sprintf("/themes/%s", slug))
	if err != nil {
		return nil, err
	}

	if data == nil {
		// Theme not found in database
		return &ThemeVulnInfo{
			Slug:            slug,
			Vulnerabilities: []Vulnerability{},
		}, nil
	}

	var info ThemeVulnInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, err
	}

	return &info, nil
}
