package cmd

import (
	"encoding/json"
	"fmt"
	stdlog "log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	serverPort          string
	domainJSONData      string
	refreshInterval     int
	serverHost          string
	configFile          string
	pathPrefix          string
	inventoryMutex      sync.RWMutex
	lastInventoryUpdate time.Time
	isRefreshing        bool
	refreshMutex        sync.Mutex
)

var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Start HTTP server to serve domain inventory data",
	Long:  `Start an HTTP server that serves the domain inventory JSON data with automatic refresh capabilities.`,
}

var serverStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the inventory HTTP server",
	RunE:  runServerStart,
}

func init() {
	rootCmd.AddCommand(serverCmd)
	serverCmd.AddCommand(serverStartCmd)

	// Server specific flags
	serverStartCmd.Flags().StringVarP(&serverPort, "port", "p", "8080", "Port to run the server on")
	serverStartCmd.Flags().StringVar(&serverHost, "host", "0.0.0.0", "Host address to bind to")
	serverStartCmd.Flags().StringVar(&domainJSONData, "domain-json-data", "", "Path to custom JSON data file (default: inventory.json)")
	serverStartCmd.Flags().IntVar(&refreshInterval, "refresh", 0, "Refresh interval in minutes (0 = no automatic refresh)")
	serverStartCmd.Flags().StringVar(&configFile, "config", "", "Path to config file (yaml)")
	serverStartCmd.Flags().StringVar(&pathPrefix, "path-prefix", "", "Path prefix for the server (e.g. /inventory)")

	viper.BindPFlag("port", serverStartCmd.Flags().Lookup("port"))
	viper.BindPFlag("host", serverStartCmd.Flags().Lookup("host"))
	viper.BindPFlag("domain-json-data", serverStartCmd.Flags().Lookup("domain-json-data"))
	viper.BindPFlag("refresh", serverStartCmd.Flags().Lookup("refresh"))
	viper.BindPFlag("path-prefix", serverStartCmd.Flags().Lookup("path-prefix"))
}

func runServerStart(cmd *cobra.Command, args []string) error {
	// Load config file if specified
	if configFile != "" {
		viper.SetConfigFile(configFile)
	} else {
		viper.SetConfigName("server-config")
		viper.AddConfigPath(".")
	}

	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			stdlog.Printf("Error reading config file: %v", err)
		}
	}

	// Update variables from viper
	serverPort = viper.GetString("port")
	serverHost = viper.GetString("host")
	domainJSONData = viper.GetString("domain-json-data")
	refreshInterval = viper.GetInt("refresh")
	pathPrefix = viper.GetString("path-prefix")

	// Determine JSON file path
	jsonFilePath := getJSONFilePath()

	// Log the file path being used
	stdlog.Printf("Looking for inventory file at: %s", jsonFilePath)

	// Check if file exists and log the status
	if _, err := os.Stat(jsonFilePath); os.IsNotExist(err) {
		if refreshInterval > 0 {
			stdlog.Printf("JSON file not found at %s, will generate on first request", jsonFilePath)
		} else {
			stdlog.Printf("Warning: JSON file not found at %s. Use --refresh flag to generate it", jsonFilePath)
			// Don't return error, let the server start and handle missing file gracefully
		}
	} else {
		stdlog.Printf("Found existing inventory file at %s", jsonFilePath)
	}

	// Start refresh goroutine if interval is set
	if refreshInterval > 0 {
		go startRefreshWorker(jsonFilePath)
	}

	// Setup router
	router := mux.NewRouter()

	// Add logging middleware
	router.Use(loggingMiddleware)

	// Create a subrouter for API routes if path prefix is set
	var apiRouter *mux.Router
	if pathPrefix != "" {
		// Ensure path prefix starts with / and doesn't end with /
		if !strings.HasPrefix(pathPrefix, "/") {
			pathPrefix = "/" + pathPrefix
		}
		pathPrefix = strings.TrimRight(pathPrefix, "/")
		stdlog.Printf("Using path prefix: %s", pathPrefix)
		apiRouter = router.PathPrefix(pathPrefix).Subrouter()
	} else {
		apiRouter = router
	}

	// API routes
	apiRouter.HandleFunc("/api/inventory", handleInventory(jsonFilePath)).Methods("GET")
	apiRouter.HandleFunc("/api/inventory/refresh", handleRefresh(jsonFilePath)).Methods("POST")
	apiRouter.HandleFunc("/api/inventory/status", handleStatus).Methods("GET")
	apiRouter.HandleFunc("/health", handleHealth).Methods("GET")

	// Serve static JSON file directly
	apiRouter.HandleFunc("/inventory.json", handleServeJSON(jsonFilePath)).Methods("GET")

	// Add a debug route to show all available routes
	apiRouter.HandleFunc("/debug/routes", handleDebugRoutes).Methods("GET")

	// Log server startup
	addr := fmt.Sprintf("%s:%s", serverHost, serverPort)
	stdlog.Printf("Starting inventory server on http://%s", addr)
	currentDir, err := os.Getwd()
	if err != nil {
		stdlog.Fatalf("Failed to get current working directory: %v", err)
	}
	stdlog.Printf("Current working directory: %s", currentDir)
	stdlog.Printf("Serving JSON file: %s", filepath.Join(currentDir, jsonFilePath))
	if refreshInterval > 0 {
		stdlog.Printf("Auto-refresh enabled: every %d minutes", refreshInterval)
	}

	stdlog.Printf("Available endpoints:")
	stdlog.Printf("  GET  /health")
	stdlog.Printf("  GET  /api/inventory")
	stdlog.Printf("  POST /api/inventory/refresh")
	stdlog.Printf("  GET  /api/inventory/status")
	stdlog.Printf("  GET  /inventory.json")
	stdlog.Printf("  GET  /debug/routes")

	// Start server
	srv := &http.Server{
		Addr:         addr,
		Handler:      router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}

	return srv.ListenAndServe()
}

func handleInventory(jsonFilePath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		inventoryMutex.RLock()
		defer inventoryMutex.RUnlock()

		// Check if file exists
		if _, err := os.Stat(jsonFilePath); os.IsNotExist(err) {
			// If refresh is enabled, trigger it and return 202
			if refreshInterval > 0 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusAccepted)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"status":  "generating",
					"message": "Inventory data is being generated. Please try again in a few moments.",
				})

				// Trigger refresh in background if not already running
				go triggerRefresh(jsonFilePath)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error":   "not_found",
				"message": fmt.Sprintf("Inventory file not found at %s", jsonFilePath),
				"path":    jsonFilePath,
			})
			return
		}

		// Read and serve the file
		data, err := os.ReadFile(jsonFilePath)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error":   "read_error",
				"message": fmt.Sprintf("Error reading inventory file: %v", err),
			})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Last-Updated", lastInventoryUpdate.Format(time.RFC3339))
		w.Write(data)
	}
}

func handleServeJSON(jsonFilePath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		inventoryMutex.RLock()
		defer inventoryMutex.RUnlock()

		// Check if file exists first
		if _, err := os.Stat(jsonFilePath); os.IsNotExist(err) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error":   "not_found",
				"message": fmt.Sprintf("Inventory file not found at %s", jsonFilePath),
				"path":    jsonFilePath,
			})
			return
		}

		http.ServeFile(w, r, jsonFilePath)
	}
}

func handleRefresh(jsonFilePath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		refreshMutex.Lock()
		if isRefreshing {
			refreshMutex.Unlock()
			w.WriteHeader(http.StatusAccepted)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"status":  "refreshing",
				"message": "Inventory refresh already in progress",
			})
			return
		}
		isRefreshing = true
		refreshMutex.Unlock()

		// Trigger refresh in background
		go func() {
			if err := refreshInventory(jsonFilePath); err != nil {
				stdlog.Printf("Error refreshing inventory: %v", err)
			}
			refreshMutex.Lock()
			isRefreshing = false
			refreshMutex.Unlock()
		}()

		w.WriteHeader(http.StatusAccepted)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":  "accepted",
			"message": "Inventory refresh initiated",
		})
	}
}

func handleStatus(w http.ResponseWriter, r *http.Request) {
	inventoryMutex.RLock()
	defer inventoryMutex.RUnlock()

	status := map[string]interface{}{
		"last_updated":     lastInventoryUpdate,
		"refresh_interval": refreshInterval,
		"is_refreshing":    isRefreshing,
		"auto_refresh":     refreshInterval > 0,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": "healthy",
	})
}

func startRefreshWorker(jsonFilePath string) {
	// Initial check/generation if file doesn't exist
	if _, err := os.Stat(jsonFilePath); os.IsNotExist(err) {
		stdlog.Println("Initial inventory generation...")
		if err := refreshInventory(jsonFilePath); err != nil {
			stdlog.Printf("Error during initial inventory generation: %v", err)
		}
	}

	// Set up ticker for periodic refresh
	ticker := time.NewTicker(time.Duration(refreshInterval) * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		stdlog.Printf("Scheduled inventory refresh starting...")
		if err := refreshInventory(jsonFilePath); err != nil {
			stdlog.Printf("Error during scheduled refresh: %v", err)
		}
	}
}

func triggerRefresh(jsonFilePath string) {
	refreshMutex.Lock()
	if isRefreshing {
		refreshMutex.Unlock()
		return
	}
	isRefreshing = true
	refreshMutex.Unlock()

	if err := refreshInventory(jsonFilePath); err != nil {
		stdlog.Printf("Error during triggered refresh: %v", err)
	}

	refreshMutex.Lock()
	isRefreshing = false
	refreshMutex.Unlock()
}

func refreshInventory(jsonFilePath string) error {
	stdlog.Println("Starting inventory refresh...")

	// Create a temporary file for the new inventory
	tempFile := jsonFilePath + ".tmp"

	// Build the inventory generate command
	// We'll use the cobra command directly to avoid external process calls
	inventoryCmd := &cobra.Command{}

	// Set the output file to our temp file
	oldOutputFile := inventoryOutputFile
	inventoryOutputFile = tempFile
	defer func() {
		inventoryOutputFile = oldOutputFile
	}()

	// Check if we have server range or local configuration
	if inventoryServerRange != "" || inventoryLocal {
		// Run the inventory command with existing configuration
		err := runInventoryGenerate(inventoryCmd, []string{})
		if err != nil {
			os.Remove(tempFile)
			return fmt.Errorf("failed to generate inventory: %w", err)
		}
	} else {
		// Default to local if no configuration
		inventoryLocal = true
		err := runInventoryGenerate(inventoryCmd, []string{})
		inventoryLocal = false
		if err != nil {
			os.Remove(tempFile)
			return fmt.Errorf("failed to generate inventory: %w", err)
		}
	}

	// Atomically replace the old file with the new one
	inventoryMutex.Lock()
	defer inventoryMutex.Unlock()

	if err := os.Rename(tempFile, jsonFilePath); err != nil {
		os.Remove(tempFile)
		return fmt.Errorf("failed to update inventory file: %w", err)
	}

	lastInventoryUpdate = time.Now()
	stdlog.Printf("Inventory refresh completed at %s", lastInventoryUpdate.Format(time.RFC3339))

	return nil
}

func getJSONFilePath() string {
	if domainJSONData != "" {
		return domainJSONData
	}
	// Use the default from inventory command
	if inventoryOutputFile != "" {
		return inventoryOutputFile
	}
	// return "inventory.json
	// Return PROJECT_ROOT/inventory.json

	// Get the project root directory
	projectRoot, err := os.Getwd()
	if err != nil {
		stdlog.Fatalf("Failed to get current working directory: %v", err)
	}
	// Print the project root for debugging
	stdlog.Printf("Project root directory: %s", projectRoot)
	// Construct the path to inventory.json
	return filepath.Join(projectRoot, "inventory.json")
}

// Add CORS middleware if needed
// func corsMiddleware(next http.Handler) http.Handler {
// 	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
// 		w.Header().Set("Access-Control-Allow-Origin", "*")
// 		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
// 		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

// 		if r.Method == "OPTIONS" {
// 			w.WriteHeader(http.StatusOK)
// 			return
// 		}

// 		next.ServeHTTP(w, r)
// 	})
// }

// Helper function to format file size
// func formatFileSize(size int64) string {
// 	const unit = 1024
// 	if size < unit {
// 		return fmt.Sprintf("%d B", size)
// 	}
// 	div, exp := int64(unit), 0
// 	for n := size / unit; n >= unit; n /= unit {
// 		div *= unit
// 		exp++
// 	}
// 	return fmt.Sprintf("%.1f %cB", float64(size)/float64(div), "KMGTPE"[exp])
// }

// Add logging middleware
func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Create a response writer wrapper to capture status code
		wrapped := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

		next.ServeHTTP(wrapped, r)

		// Check for X-Forwarded-For header
		clientIP := r.Header.Get("X-Forwarded-For")
		if clientIP == "" {
			clientIP = r.RemoteAddr
		}

		stdlog.Printf("%s %s %d %v (IP: %s)", r.Method, r.URL.Path, wrapped.statusCode, time.Since(start), clientIP)
	})
}

type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// Debug route to show available routes
func handleDebugRoutes(w http.ResponseWriter, r *http.Request) {
	routes := []string{
		"GET  /health",
		"GET  /api/inventory",
		"POST /api/inventory/refresh",
		"GET  /api/inventory/status",
		"GET  /inventory.json",
		"GET  /debug/routes",
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"available_routes": routes,
		"server_status":    "running",
	})
}
