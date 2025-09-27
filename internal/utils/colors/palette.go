package colors

import (
	"fmt"
	"math/rand"
	"sort"
	"time"
)

// TailwindColorConfig represents the final structured color palette for a Tailwind CSS config.
type TailwindColorConfig struct {
	Primary   string `json:"primary"`
	Secondary string `json:"secondary"`
	Accent    string `json:"accent"`
	Neutral   string `json:"neutral"`
	Base      string `json:"base"`
}

// GeneratePaletteConfig is the main algorithm to generate a Tailwind config and an HTML preview from a list of hex colors.
func GeneratePaletteConfig(hexColors []string) (*TailwindColorConfig, string, error) {
	if len(hexColors) < 2 {
		return nil, "", fmt.Errorf("at least 2 colors are required to generate a palette")
	}

	// If fewer than 5 colors, generate the missing ones.
	if len(hexColors) < 5 {
		hexColors = completePalette(hexColors)
	}

	// Create Color objects from hex strings
	var palette []*Color
	for _, hex := range hexColors {
		c, err := NewColor(hex)
		if err != nil {
			return nil, "", err
		}
		palette = append(palette, c)
	}

	// 1. Identify the darkest and lightest colors.
	sort.Slice(palette, func(i, j int) bool {
		return palette[i].Luminance() < palette[j].Luminance()
	})

	darkest := palette[0]
	lightest := palette[len(palette)-1]
	remaining := palette[1 : len(palette)-1]

	config := &TailwindColorConfig{}

	// 2. Assign primary/secondary with more variety
	rand.Seed(time.Now().UnixNano())

	// Sometimes use middle colors instead of just darkest/lightest for more variety
	assignmentStrategy := rand.Intn(4)
	switch assignmentStrategy {
	case 0, 1: // 50% chance: traditional darkest/lightest assignment
		if rand.Intn(2) == 0 {
			config.Primary = darkest.Hex
			config.Secondary = lightest.Hex
		} else {
			config.Primary = lightest.Hex
			config.Secondary = darkest.Hex
		}
	case 2: // 25% chance: use middle tones for more subtle palettes
		if len(remaining) >= 2 {
			config.Primary = remaining[rand.Intn(len(remaining))].Hex
			config.Secondary = remaining[rand.Intn(len(remaining))].Hex
			// Ensure they're different
			for config.Primary == config.Secondary && len(remaining) > 1 {
				config.Secondary = remaining[rand.Intn(len(remaining))].Hex
			}
		} else {
			// Fallback to darkest/lightest
			config.Primary = darkest.Hex
			config.Secondary = lightest.Hex
		}
	case 3: // 25% chance: mix extreme with middle
		if len(remaining) > 0 {
			if rand.Intn(2) == 0 {
				config.Primary = darkest.Hex
				config.Secondary = remaining[rand.Intn(len(remaining))].Hex
			} else {
				config.Primary = lightest.Hex
				config.Secondary = remaining[rand.Intn(len(remaining))].Hex
			}
		} else {
			config.Primary = darkest.Hex
			config.Secondary = lightest.Hex
		}
	}

	// Get primary/secondary as Color objects for comparison
	primaryColor, _ := NewColor(config.Primary)
	secondaryColor, _ := NewColor(config.Secondary)

	// 3. Assign remaining colors with some randomization for variety
	if len(remaining) >= 3 {
		// Sort by contrast but with some randomization
		sort.Slice(remaining, func(i, j int) bool {
			distI := ColorDifference(remaining[i], primaryColor) + ColorDifference(remaining[i], secondaryColor)
			distJ := ColorDifference(remaining[j], primaryColor) + ColorDifference(remaining[j], secondaryColor)

			// Add some randomness: 20% chance to ignore optimal sorting
			if rand.Intn(5) == 0 {
				return rand.Intn(2) == 0
			}
			return distI > distJ // Sort descending by distance
		})

		// Choose accent from top 2 colors (adds variety while maintaining quality)
		accentChoice := 0
		if len(remaining) > 1 && rand.Intn(3) == 0 { // 33% chance to use second-best
			accentChoice = 1
		}
		config.Accent = remaining[accentChoice].Hex

		// Assign base and neutral with more variation
		rem1, rem2 := remaining[1], remaining[2]
		if accentChoice == 1 && len(remaining) > 2 {
			rem1, rem2 = remaining[0], remaining[2]
		}

		// Sometimes prioritize aesthetics over pure contrast
		assignmentMethod := rand.Intn(3)
		switch assignmentMethod {
		case 0: // Traditional contrast-based assignment
			contrast1 := ColorDifference(rem1, primaryColor)
			contrast2 := ColorDifference(rem2, primaryColor)
			if contrast1 > contrast2 {
				config.Base = rem1.Hex
				config.Neutral = rem2.Hex
			} else {
				config.Base = rem2.Hex
				config.Neutral = rem1.Hex
			}
		case 1: // Random assignment for more variety
			if rand.Intn(2) == 0 {
				config.Base = rem1.Hex
				config.Neutral = rem2.Hex
			} else {
				config.Base = rem2.Hex
				config.Neutral = rem1.Hex
			}
		case 2: // Luminance-based assignment
			if rem1.Luminance() > rem2.Luminance() {
				config.Base = rem1.Hex    // Lighter color as base
				config.Neutral = rem2.Hex // Darker color as neutral
			} else {
				config.Base = rem2.Hex
				config.Neutral = rem1.Hex
			}
		}
	} else {
		// Fallback for insufficient colors
		if len(remaining) > 0 {
			config.Accent = remaining[0].Hex
		}
		if len(remaining) > 1 {
			config.Base = remaining[1].Hex
			config.Neutral = lightest.Hex
		} else {
			config.Base = lightest.Hex
			config.Neutral = darkest.Hex
		}
	}

	// Generate the HTML preview page
	htmlPreview, err := generateHTMLPreview(config)
	if err != nil {
		return nil, "", fmt.Errorf("failed to generate HTML preview: %w", err)
	}

	return config, htmlPreview, nil
}

// generateHTMLPreview creates a self-contained HTML file to visualize the color palette.
func generateHTMLPreview(config *TailwindColorConfig) (string, error) {
	// Determine text colors for each background for contrast
	primaryText := getTextColorForBackground(config.Primary)
	secondaryText := getTextColorForBackground(config.Secondary)
	neutralText := getTextColorForBackground(config.Neutral)
	baseText := getTextColorForBackground(config.Base) // This will be the main text color

	// Using Sprintf to format the HTML string is clear and sufficient for this task.
	htmlTemplate := `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Color Palette Preview</title>
    <style>
        :root {
            --primary-color: %s;
            --secondary-color: %s;
            --accent-color: %s;
            --neutral-color: %s;
            --base-color: %s;
            
            --primary-text: %s;
            --secondary-text: %s;
            --neutral-text: %s;
            --base-text: %s;
        }

        /* Reset and basic setup */
        body, html {
            margin: 0;
            padding: 0;
            font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif, "Apple Color Emoji", "Segoe UI Emoji", "Segoe UI Symbol";
            background-color: var(--base-color);
            color: var(--base-text);
            line-height: 1.6;
            transition: background-color 0.3s, color 0.3s;
        }

        .container {
            max-width: 1100px;
            margin: 0 auto;
            padding: 0 20px;
        }

        /* Header and Nav */
        .header {
            background-color: var(--neutral-color);
            color: var(--neutral-text);
            padding: 1rem 0;
            box-shadow: 0 2px 4px rgba(0,0,0,0.1);
        }

        .header .container {
            display: flex;
            justify-content: space-between;
            align-items: center;
        }
        
        .logo { font-size: 1.5rem; font-weight: bold; }
        
        .nav a {
            color: var(--neutral-text);
            text-decoration: none;
            margin-left: 20px;
            transition: color 0.3s;
            font-weight: 500;
        }
        .nav a:hover {
            color: var(--accent-color);
        }

        /* Hero Section */
        .hero {
            background-color: var(--secondary-color);
            color: var(--secondary-text);
            padding: 5rem 0;
            text-align: center;
        }

        .hero h1 {
            font-size: 3rem;
            margin-bottom: 1rem;
        }

        .hero p {
            font-size: 1.2rem;
            opacity: 0.9;
        }
        
        .hero-image-placeholder {
            width: 80%%;
            height: 300px;
            margin: 2rem auto 0;
            background-color: var(--primary-color);
            display: flex;
            align-items: center;
            justify-content: center;
            font-size: 1.2rem;
            color: var(--primary-text);
            border-radius: 8px;
            box-shadow: 0 4px 15px rgba(0,0,0,0.2);
        }

        /* Main Content */
        .main-content {
            padding: 4rem 0;
        }
        
        .main-content h2 { 
            color: var(--primary-color); 
            border-bottom: 3px solid var(--accent-color);
            padding-bottom: 0.5rem;
            display: inline-block;
        }
        .main-content a { color: var(--accent-color); font-weight: bold; text-decoration: none; }
        .main-content a:hover { text-decoration: underline; }

        /* Footer */
        .footer {
            background-color: var(--primary-color);
            color: var(--primary-text);
            padding: 3rem 0;
            margin-top: 2rem;
        }
        
        .footer-grid {
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
            gap: 2rem;
        }
        
        .footer-col h4 {
            margin-top: 0;
            border-bottom: 2px solid var(--accent-color);
            padding-bottom: 0.5rem;
            display: inline-block;
        }
        
        .footer-col ul { list-style: none; padding: 0; }
        .footer-col li { margin-bottom: 0.5rem; }
        .footer-col a { color: var(--primary-text); text-decoration: none; opacity: 0.8; }
        .footer-col a:hover { opacity: 1; text-decoration: underline; }

    </style>
</head>
<body>
    <header class="header">
        <div class="container">
            <div class="logo">Company Inc.</div>
            <nav class="nav">
                <a href="#">Home</a>
                <a href="#">About</a>
                <a href="#">Services</a>
                <a href="#">Contact</a>
            </nav>
        </div>
    </header>

    <section class="hero">
        <div class="container">
            <h1>A Modern Web Page Design</h1>
            <p>This page demonstrates the generated color palette in a professional layout.</p>
            <div class="hero-image-placeholder">
                <span>Hero Image Placeholder</span>
            </div>
        </div>
    </section>

    <main class="main-content">
        <div class="container">
            <h2>Demonstrating the Palette</h2>
            <p>Lorem ipsum dolor sit amet, consectetur adipiscing elit. Sed non risus. Suspendisse lectus tortor, dignissim sit amet, adipiscing nec, ultricies sed, dolor. Cras elementum ultrices diam. Maecenas ligula massa, varius a, semper congue, euismod non, mi. Proin porttitor, orci nec nonummy molestie, enim est eleifend mi, non fermentum diam nisl sit amet erat. <a href="#">This is an accent link.</a></p>
            <p>Duis semper. Duis arcu massa, scelerisque vitae, consequat in, pretium a, enim. Pellentesque congue. Ut in risus volutpat libero pharetra tempor. Cras vestibulum bibendum augue. Praesent egestas leo in pede. Praesent blandit odio eu enim. Pellentesque sed dui ut augue blandit sodales. Vestibulum ante ipsum primis in faucibus orci luctus et ultrices posuere cubilia Curae; Aliquam nibh.</p>
        </div>
    </main>

    <footer class="footer">
        <div class="container">
            <div class="footer-grid">
                <div class="footer-col">
                    <h4>Products</h4>
                    <ul>
                        <li><a href="#">Lorem Ipsum</a></li>
                        <li><a href="#">Dolor Sit</a></li>
                        <li><a href="#">Amet Consectetur</a></li>
                        <li><a href="#">Adipiscing Elit</a></li>
                    </ul>
                </div>
                <div class="footer-col">
                    <h4>Resources</h4>
                    <ul>
                        <li><a href="#">Sed Non Risus</a></li>
                        <li><a href="#">Suspendisse Lectus</a></li>
                        <li><a href="#">Tortor Dignissim</a></li>
                    </ul>
                </div>
                <div class="footer-col">
                    <h4>Company</h4>
                    <ul>
                        <li><a href="#">Cras Elementum</a></li>
                        <li><a href="#">Ultrices Diam</a></li>
                        <li><a href="#">Maecenas Ligula</a></li>
                        <li><a href="#">Massa Varius</a></li>
                    </ul>
                </div>
                <div class="footer-col">
                    <h4>Legal</h4>
                    <ul>
                        <li><a href="#">Semper Congue</a></li>
                        <li><a href="#">Euismod Non</a></li>
                    </ul>
                </div>
            </div>
        </div>
    </footer>
</body>
</html>`
	return fmt.Sprintf(htmlTemplate,
		config.Primary,
		config.Secondary,
		config.Accent,
		config.Neutral,
		config.Base,
		primaryText,
		secondaryText,
		neutralText,
		baseText,
	), nil
}

// completePalette generates missing colors if the input palette has fewer than 5.
// Uses color theory and randomization to create diverse, quality palettes.
func completePalette(colors []string) []string {
	fmt.Printf("--- Notice: Less than 5 colors provided. Generating %d additional colors. ---\n", 5-len(colors))

	// Seed random number generator with current time for variety
	rand.Seed(time.Now().UnixNano())

	// Create a set of existing colors to avoid duplicates
	existing := make(map[string]bool)
	for _, c := range colors {
		existing[c] = true
	}

	// Parse existing colors to understand their characteristics
	var existingColors []*Color
	for _, hex := range colors {
		if c, err := NewColor(hex); err == nil {
			existingColors = append(existingColors, c)
		}
	}

	// Generate colors with more variety and better distribution
	colorGenerators := []func() string{
		func() string { // Complementary neutrals
			base := rand.Intn(40) + 40     // 40-79 range
			variation := rand.Intn(15) - 7 // -7 to +7 variation
			return fmt.Sprintf("#%02x%02x%02x", base+variation, base+variation, base+variation)
		},
		func() string { // Light accent colors
			return fmt.Sprintf("#%02x%02x%02x", rand.Intn(60)+180, rand.Intn(60)+180, rand.Intn(60)+180)
		},
		func() string { // Medium tones with slight color bias
			base := rand.Intn(80) + 80 // 80-159 range
			switch rand.Intn(3) {
			case 0: // Warm bias
				return fmt.Sprintf("#%02x%02x%02x", base+rand.Intn(40), base-rand.Intn(20), base-rand.Intn(30))
			case 1: // Cool bias
				return fmt.Sprintf("#%02x%02x%02x", base-rand.Intn(30), base-rand.Intn(20), base+rand.Intn(40))
			default: // Neutral with slight variation
				return fmt.Sprintf("#%02x%02x%02x", base+rand.Intn(20)-10, base+rand.Intn(20)-10, base+rand.Intn(20)-10)
			}
		},
		func() string { // Dark colors for depth
			return fmt.Sprintf("#%02x%02x%02x", rand.Intn(60)+20, rand.Intn(60)+20, rand.Intn(60)+20)
		},
		func() string { // Saturated accent colors
			switch rand.Intn(4) {
			case 0: // Blue family
				return fmt.Sprintf("#%02x%02x%02x", rand.Intn(80)+20, rand.Intn(100)+80, rand.Intn(100)+120)
			case 1: // Orange/Red family
				return fmt.Sprintf("#%02x%02x%02x", rand.Intn(120)+120, rand.Intn(80)+60, rand.Intn(40)+20)
			case 2: // Green family
				return fmt.Sprintf("#%02x%02x%02x", rand.Intn(80)+40, rand.Intn(120)+80, rand.Intn(80)+60)
			default: // Purple family
				return fmt.Sprintf("#%02x%02x%02x", rand.Intn(100)+80, rand.Intn(60)+40, rand.Intn(100)+100)
			}
		},
	}

	// Add colors until we have 5, using different generators for variety
	generatorIndex := 0
	attempts := 0
	maxAttempts := 50 // Prevent infinite loops

	for len(colors) < 5 && attempts < maxAttempts {
		// Use a different generator each time, cycling through them
		generator := colorGenerators[generatorIndex%len(colorGenerators)]
		hex := generator()

		if !existing[hex] {
			// Verify the color is different enough from existing ones
			newColor, err := NewColor(hex)
			if err == nil {
				tooSimilar := false
				for _, existing := range existingColors {
					if ColorDifference(newColor, existing) < 30 { // Minimum difference threshold
						tooSimilar = true
						break
					}
				}

				if !tooSimilar {
					fmt.Printf("--- Added generated color (%s) to complete the palette. ---\n", hex)
					colors = append(colors, hex)
					existing[hex] = true
					existingColors = append(existingColors, newColor)
					generatorIndex++
				}
			}
		}
		attempts++
	}

	// Fallback: if we still don't have enough colors, add truly random ones
	for len(colors) < 5 {
		hex := fmt.Sprintf("#%06x", rand.Intn(0xFFFFFF))
		if !existing[hex] {
			fmt.Printf("--- Added random fallback color (%s) to complete the palette. ---\n", hex)
			colors = append(colors, hex)
			existing[hex] = true
		}
	}

	return colors
}
