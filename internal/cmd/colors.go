package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ciwg-cli/internal/utils/colors"

	"github.com/spf13/cobra"
)

var colorsCmd = &cobra.Command{
	Use:   "colors",
	Short: "Utilities for generating color palettes",
	Long:  `A collection of tools for creating and managing color palettes for Tailwind CSS.`,
}

var generatePaletteCmd = &cobra.Command{
	Use:   "generate [hostname]",
	Short: "Generate a Tailwind CSS color palette and HTML preview",
	Long: `Generates a Tailwind CSS color configuration and an HTML preview from a given color palette.
The generated files can be written to a remote server via SSH.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runGeneratePalette,
}

func init() {
	rootCmd.AddCommand(colorsCmd)
	colorsCmd.AddCommand(generatePaletteCmd)

	// --- Command-specific flags ---
	generatePaletteCmd.Flags().String("palette", "#14213d,#fca311,#e5e5e5,#000000,#ffffff", "Comma-separated list of hex colors for the palette")
	generatePaletteCmd.Flags().String("output-path", "/var/www/html", "Remote directory path to save the generated files")
	generatePaletteCmd.Flags().String("config-name", "tailwind.colors.json", "Filename for the generated Tailwind JSON config")
	generatePaletteCmd.Flags().String("html-name", "palette-preview.html", "Filename for the generated HTML preview")
	generatePaletteCmd.Flags().String("server-range", "", "Server range pattern (e.g., 'wp%d.example.com:0-41')")

	// Add SSH connection flags
	generatePaletteCmd.Flags().StringP("user", "u", "", "SSH username (default: current user)")
	generatePaletteCmd.Flags().StringP("port", "p", "22", "SSH port")
	generatePaletteCmd.Flags().StringP("key", "k", "", "Path to SSH private key")
	generatePaletteCmd.Flags().BoolP("agent", "a", true, "Use SSH agent")
	generatePaletteCmd.Flags().DurationP("timeout", "t", 30*time.Second, "Connection timeout")
}

func runGeneratePalette(cmd *cobra.Command, args []string) error {
	serverRange, _ := cmd.Flags().GetString("server-range")

	if serverRange != "" {
		return processGeneratePaletteForServerRange(cmd, serverRange)
	}

	if len(args) < 1 {
		return fmt.Errorf("hostname argument is required when --server-range is not used")
	}

	hostname := args[0]
	return runGeneratePaletteOnServer(cmd, hostname)
}

func processGeneratePaletteForServerRange(cmd *cobra.Command, serverRange string) error {
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
		fmt.Printf("--- Processing server: %s ---\n", hostname)
		err := runGeneratePaletteOnServer(cmd, hostname)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error processing %s: %v\n", hostname, err)
		}
		fmt.Println()
	}

	return nil
}

func runGeneratePaletteOnServer(cmd *cobra.Command, hostname string) error {
	// --- Get flag values ---
	paletteStr, _ := cmd.Flags().GetString("palette")
	outputPath, _ := cmd.Flags().GetString("output-path")
	configName, _ := cmd.Flags().GetString("config-name")
	htmlName, _ := cmd.Flags().GetString("html-name")

	// --- Generate Palette ---
	palette := strings.Split(paletteStr, ",")
	fmt.Printf("[%s] Generating palette with colors: %v\n", hostname, palette)

	config, htmlPreview, err := colors.GeneratePaletteConfig(palette)
	if err != nil {
		return fmt.Errorf("failed to generate palette for %s: %w", hostname, err)
	}

	// Marshal the config to JSON
	prettyJSON, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to generate JSON for %s: %w", hostname, err)
	}

	// --- Connect via SSH ---
	client, err := createSSHClient(cmd, hostname)
	if err != nil {
		return fmt.Errorf("failed to create SSH client for %s: %w", hostname, err)
	}
	defer client.Close()

	fmt.Printf("[%s] Connected via SSH. Writing files to %s...\n", hostname, outputPath)

	// --- Write files to remote server ---
	// 1. Write Tailwind config
	configPath := filepath.Join(outputPath, configName)
	// Use single quotes to wrap the JSON to avoid issues with shell interpretation
	writeCmd := fmt.Sprintf("echo '%s' > %s", string(prettyJSON), configPath)
	_, stderr, err := client.ExecuteCommand(writeCmd)
	if err != nil {
		return fmt.Errorf("failed to write Tailwind config to %s: %w\nStderr: %s", hostname, err, stderr)
	}
	fmt.Printf("[%s] ✓ Successfully wrote Tailwind config to %s\n", hostname, configPath)

	// 2. Write HTML preview
	htmlPath := filepath.Join(outputPath, htmlName)
	// Use single quotes to wrap the HTML
	writeCmd = fmt.Sprintf("echo '%s' > %s", htmlPreview, htmlPath)
	_, stderr, err = client.ExecuteCommand(writeCmd)
	if err != nil {
		return fmt.Errorf("failed to write HTML preview to %s: %w\nStderr: %s", hostname, err, stderr)
	}
	fmt.Printf("[%s] ✓ Successfully wrote HTML preview to %s\n", hostname, htmlPath)

	fmt.Printf("[%s] Palette generation and remote deployment complete!\n", hostname)
	return nil
}
