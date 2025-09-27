package cmd

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
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
	generatePaletteCmd.Flags().String("palette", "", "Comma-separated list of hex colors for the palette (if empty, generates random base colors)")
	generatePaletteCmd.Flags().String("output-path", "/var/www/html", "Remote directory path to save the generated files")
	generatePaletteCmd.Flags().String("config-name", "tailwind.colors.json", "Filename for the generated Tailwind JSON config")
	generatePaletteCmd.Flags().String("html-name", "palette-preview.html", "Filename for the generated HTML preview")
	generatePaletteCmd.Flags().String("server-range", "", "Server range pattern (e.g., 'wp%d.example.com:0-41')")
	generatePaletteCmd.Flags().String("set-primary-color", "", "Set primary color (hex code or 'random' for slight variation from black/white)")
	generatePaletteCmd.Flags().String("set-secondary-color", "", "Set secondary color (hex code or 'random' for slight variation from black/white)")

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

	// Allow local execution if no hostname is provided
	if len(args) == 0 {
		return runGeneratePaletteLocally(cmd)
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
	primaryColor, _ := cmd.Flags().GetString("set-primary-color")
	secondaryColor, _ := cmd.Flags().GetString("set-secondary-color")

	// --- Process color overrides ---
	primaryColorValue := processColorFlag(primaryColor)
	secondaryColorValue := processColorFlag(secondaryColor)

	// --- Generate Palette ---
	var palette []string
	if paletteStr == "" {
		// Generate a completely random base palette
		palette = generateRandomBasePalette()
		fmt.Printf("[%s] ✓ Generated random base palette\n", hostname)
	} else {
		palette = strings.Split(paletteStr, ",")
		// Clean up any empty strings from splitting
		var cleanPalette []string
		for _, color := range palette {
			if strings.TrimSpace(color) != "" {
				cleanPalette = append(cleanPalette, strings.TrimSpace(color))
			}
		}
		palette = cleanPalette

		// If after cleaning we have no valid colors, generate random ones
		if len(palette) == 0 {
			palette = generateRandomBasePalette()
			fmt.Printf("[%s] ✓ Generated random base palette (no valid colors provided)\n", hostname)
		}
	}

	// If both primary and secondary colors are set, we can bypass the 2-color requirement
	if primaryColorValue != "" && secondaryColorValue != "" && len(palette) < 2 {
		// Add the override colors to ensure we meet minimum requirements
		palette = append(palette, primaryColorValue, secondaryColorValue)
		fmt.Printf("[%s] ✓ Using color overrides to fulfill palette requirements\n", hostname)
	}

	fmt.Printf("[%s] Generating palette with colors: %v\n", hostname, palette)

	config, htmlPreview, err := colors.GeneratePaletteConfig(palette)
	if err != nil {
		return fmt.Errorf("failed to generate palette for %s: %w", hostname, err)
	}

	// --- Apply color overrides ---
	if primaryColorValue != "" {
		config.Primary = primaryColorValue
		fmt.Printf("[%s] ✓ Primary color set to: %s\n", hostname, primaryColorValue)
	}
	if secondaryColorValue != "" {
		config.Secondary = secondaryColorValue
		fmt.Printf("[%s] ✓ Secondary color set to: %s\n", hostname, secondaryColorValue)
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

func runGeneratePaletteLocally(cmd *cobra.Command) error {
	// --- Get flag values ---
	paletteStr, _ := cmd.Flags().GetString("palette")
	outputPath, _ := cmd.Flags().GetString("output-path")
	configName, _ := cmd.Flags().GetString("config-name")
	htmlName, _ := cmd.Flags().GetString("html-name")
	primaryColor, _ := cmd.Flags().GetString("set-primary-color")
	secondaryColor, _ := cmd.Flags().GetString("set-secondary-color")

	// --- Process color overrides ---
	primaryColorValue := processColorFlag(primaryColor)
	secondaryColorValue := processColorFlag(secondaryColor)

	// --- Generate Palette ---
	var palette []string
	if paletteStr == "" {
		// Generate a completely random base palette
		palette = generateRandomBasePalette()
		fmt.Printf("[local] ✓ Generated random base palette\n")
	} else {
		palette = strings.Split(paletteStr, ",")
		// Clean up any empty strings from splitting
		var cleanPalette []string
		for _, color := range palette {
			if strings.TrimSpace(color) != "" {
				cleanPalette = append(cleanPalette, strings.TrimSpace(color))
			}
		}
		palette = cleanPalette

		// If after cleaning we have no valid colors, generate random ones
		if len(palette) == 0 {
			palette = generateRandomBasePalette()
			fmt.Printf("[local] ✓ Generated random base palette (no valid colors provided)\n")
		}
	}

	// If both primary and secondary colors are set, we can bypass the 2-color requirement
	if primaryColorValue != "" && secondaryColorValue != "" && len(palette) < 2 {
		// Add the override colors to ensure we meet minimum requirements
		palette = append(palette, primaryColorValue, secondaryColorValue)
		fmt.Printf("[local] ✓ Using color overrides to fulfill palette requirements\n")
	}

	fmt.Printf("[local] Generating palette with colors: %v\n", palette)

	config, htmlPreview, err := colors.GeneratePaletteConfig(palette)
	if err != nil {
		return fmt.Errorf("failed to generate palette locally: %w", err)
	}

	// --- Apply color overrides ---
	if primaryColorValue != "" {
		config.Primary = primaryColorValue
		fmt.Printf("[local] ✓ Primary color set to: %s\n", primaryColorValue)
	}
	if secondaryColorValue != "" {
		config.Secondary = secondaryColorValue
		fmt.Printf("[local] ✓ Secondary color set to: %s\n", secondaryColorValue)
	}

	// Marshal the config to JSON
	prettyJSON, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to generate JSON locally: %w", err)
	}

	fmt.Printf("[local] Writing files to %s...\n", outputPath)

	// Ensure output directory exists
	if err := os.MkdirAll(outputPath, 0755); err != nil {
		return fmt.Errorf("failed to create output directory %s: %w", outputPath, err)
	}

	// --- Write files locally ---
	// 1. Write Tailwind config
	configPath := filepath.Join(outputPath, configName)
	if err := os.WriteFile(configPath, prettyJSON, 0644); err != nil {
		return fmt.Errorf("failed to write Tailwind config to %s: %w", configPath, err)
	}
	fmt.Printf("[local] ✓ Successfully wrote Tailwind config to %s\n", configPath)

	// 2. Write HTML preview
	htmlPath := filepath.Join(outputPath, htmlName)
	if err := os.WriteFile(htmlPath, []byte(htmlPreview), 0644); err != nil {
		return fmt.Errorf("failed to write HTML preview to %s: %w", htmlPath, err)
	}
	fmt.Printf("[local] ✓ Successfully wrote HTML preview to %s\n", htmlPath)

	fmt.Printf("[local] Palette generation and local deployment complete!\n", outputPath)
	return nil
}

// isValidHexColor checks if the given string is a valid hex color code
func isValidHexColor(hexColor string) bool {
	hexPattern := regexp.MustCompile("^#[0-9a-fA-F]{6}$")
	return hexPattern.MatchString(hexColor)
}

// generateRandomColor generates a random color that varies slightly from black (#000000) or white (#ffffff)
func generateRandomColor() string {
	rand.Seed(time.Now().UnixNano())

	// Randomly choose between black and white as base
	if rand.Intn(2) == 0 {
		// Vary from black (#000000) - add small random values
		r := rand.Intn(40) // 0-39
		g := rand.Intn(40) // 0-39
		b := rand.Intn(40) // 0-39
		return fmt.Sprintf("#%02x%02x%02x", r, g, b)
	} else {
		// Vary from white (#ffffff) - subtract small random values
		r := 255 - rand.Intn(40) // 216-255
		g := 255 - rand.Intn(40) // 216-255
		b := 255 - rand.Intn(40) // 216-255
		return fmt.Sprintf("#%02x%02x%02x", r, g, b)
	}
}

// processColorFlag processes a color flag value and returns the appropriate hex color
func processColorFlag(flagValue string) string {
	if flagValue == "" {
		return ""
	}

	if isValidHexColor(flagValue) {
		return flagValue
	}

	// If not a valid hex color, generate a random color
	return generateRandomColor()
}

// generateRandomBasePalette creates a random starting palette following the grayscale + professional pattern
func generateRandomBasePalette() []string {
	rand.Seed(time.Now().UnixNano())

	palette := make([]string, 0, 3)

	// Always start with 1-2 grayscale colors (for primary/secondary)
	// Generate dark grayscale
	darkBase := rand.Intn(50) + 10     // 10-59 range for dark colors
	darkVariation := rand.Intn(10) - 5 // Small variation for naturalness
	darkVal := darkBase + darkVariation
	if darkVal < 0 {
		darkVal = 0
	}
	if darkVal > 60 {
		darkVal = 60
	}
	darkColor := fmt.Sprintf("#%02x%02x%02x", darkVal, darkVal, darkVal)
	palette = append(palette, darkColor)

	// Generate light grayscale (70% chance) or middle gray (30% chance)
	if rand.Intn(10) < 7 {
		// Light grayscale
		lightBase := rand.Intn(50) + 180 // 180-229 range for light colors
		lightVariation := rand.Intn(10) - 5
		lightVal := lightBase + lightVariation
		if lightVal < 150 {
			lightVal = 150
		}
		if lightVal > 255 {
			lightVal = 255
		}
		lightColor := fmt.Sprintf("#%02x%02x%02x", lightVal, lightVal, lightVal)
		palette = append(palette, lightColor)
	} else {
		// Middle gray
		midBase := rand.Intn(60) + 80 // 80-139 range
		midVariation := rand.Intn(10) - 5
		midVal := midBase + midVariation
		if midVal < 70 {
			midVal = 70
		}
		if midVal > 150 {
			midVal = 150
		}
		midColor := fmt.Sprintf("#%02x%02x%02x", midVal, midVal, midVal)
		palette = append(palette, midColor)
	}

	// Optionally add 1 professional color for variety (completePalette will fill the rest)
	if rand.Intn(2) == 0 {
		colorFamily := rand.Intn(6)
		var professionalColor string
		switch colorFamily {
		case 0: // Muted blues (most professional)
			r := rand.Intn(60) + 30  // 30-89
			g := rand.Intn(80) + 70  // 70-149
			b := rand.Intn(100) + 90 // 90-189
			professionalColor = fmt.Sprintf("#%02x%02x%02x", r, g, b)
		case 1: // Earth tones
			r := rand.Intn(80) + 70 // 70-149
			g := rand.Intn(60) + 60 // 60-119
			b := rand.Intn(50) + 40 // 40-89
			professionalColor = fmt.Sprintf("#%02x%02x%02x", r, g, b)
		case 2: // Muted greens
			r := rand.Intn(60) + 50  // 50-109
			g := rand.Intn(100) + 80 // 80-179
			b := rand.Intn(80) + 60  // 60-139
			professionalColor = fmt.Sprintf("#%02x%02x%02x", r, g, b)
		case 3: // Desaturated teals
			r := rand.Intn(50) + 40 // 40-89
			g := rand.Intn(80) + 90 // 90-169
			b := rand.Intn(80) + 80 // 80-159
			professionalColor = fmt.Sprintf("#%02x%02x%02x", r, g, b)
		case 4: // Muted warm colors
			r := rand.Intn(100) + 110 // 110-209
			g := rand.Intn(70) + 70   // 70-139
			b := rand.Intn(50) + 50   // 50-99
			professionalColor = fmt.Sprintf("#%02x%02x%02x", r, g, b)
		default: // Neutral with color cast
			base := rand.Intn(60) + 70 // 70-129
			cast := rand.Intn(15) - 7  // -7 to +7
			professionalColor = fmt.Sprintf("#%02x%02x%02x", base+cast, base, base-cast)
		}
		palette = append(palette, professionalColor)
	}

	return palette
}
