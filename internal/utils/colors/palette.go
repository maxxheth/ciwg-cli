package colors

import (
	"fmt"
	"math"
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

// HSL represents the Hue, Saturation, Lightness color model
type HSL struct {
	H, S, L float64
}

// ToHSL converts a Color to HSL representation
func (c *Color) ToHSL() HSL {
	r := float64(c.RGB.R) / 255.0
	g := float64(c.RGB.G) / 255.0
	b := float64(c.RGB.B) / 255.0

	max := math.Max(r, math.Max(g, b))
	min := math.Min(r, math.Min(g, b))
	diff := max - min

	// Lightness
	l := (max + min) / 2.0

	var h, s float64

	if diff == 0 {
		h = 0
		s = 0 // achromatic (gray)
	} else {
		// Saturation
		if l < 0.5 {
			s = diff / (max + min)
		} else {
			s = diff / (2.0 - max - min)
		}

		// Hue
		switch max {
		case r:
			h = (g - b) / diff
			if g < b {
				h += 6
			}
		case g:
			h = (b-r)/diff + 2
		case b:
			h = (r-g)/diff + 4
		}
		h /= 6.0
	}

	return HSL{H: h * 360, S: s, L: l}
}

// isGrayscale determines if a color is grayscale/neutral based on saturation
func isGrayscale(color *Color) bool {
	hsl := color.ToHSL()
	// A color is considered grayscale if saturation is very low (< 0.15)
	return hsl.S < 0.15
}

// generateGrayscaleColor creates a grayscale color much closer to true black or white
func generateGrayscaleColor(minLum, maxLum float64) string {
	var val int

	if maxLum <= 0.3 {
		// Generate very dark colors (close to black) - 0-40 range
		val = rand.Intn(35) + 5 // 5-39
	} else {
		// Generate very light colors (close to white) - 220-255 range
		val = rand.Intn(35) + 220 // 220-254
	}

	return fmt.Sprintf("#%02x%02x%02x", val, val, val)
}

// generateProfessionalColor creates distinct, saturated colors like in the examples
func generateProfessionalColor(colorFamily int) string {
	switch colorFamily {
	case 0: // Rich blues (oxford_blue, yale_blue, space_cadet style)
		r := rand.Intn(60) + 3   // 3-62 (dark)
		g := rand.Intn(80) + 20  // 20-99
		b := rand.Intn(120) + 60 // 60-179 (blue dominant)
		return fmt.Sprintf("#%02x%02x%02x", r, g, b)
	case 1: // Warm oranges/reds (orange_web, fire_engine_red style)
		r := rand.Intn(120) + 135 // 135-254 (red dominant)
		g := rand.Intn(100) + 30  // 30-129
		b := rand.Intn(60) + 10   // 10-69 (minimal blue)
		return fmt.Sprintf("#%02x%02x%02x", r, g, b)
	case 2: // Rich greens (hooker's_green, cambridge_blue style)
		r := rand.Intn(100) + 20 // 20-119
		g := rand.Intn(140) + 80 // 80-219 (green dominant)
		b := rand.Intn(120) + 40 // 40-159
		return fmt.Sprintf("#%02x%02x%02x", r, g, b)
	case 3: // Deep teals/cyans (caribbean_current, verdigris style)
		r := rand.Intn(70) + 10  // 10-79 (minimal red)
		g := rand.Intn(120) + 70 // 70-189 (cyan components)
		b := rand.Intn(120) + 70 // 70-189
		return fmt.Sprintf("#%02x%02x%02x", r, g, b)
	case 4: // Earth tones (taupe, walnut_brown, khaki style)
		base := rand.Intn(100) + 70    // 70-169
		r := base + rand.Intn(30) - 15 // Slight red bias
		g := base + rand.Intn(20) - 10 // Neutral green
		b := base - rand.Intn(40)      // Reduced blue for earth tone
		if r > 255 {
			r = 255
		}
		if r < 30 {
			r = 30
		}
		if g > 255 {
			g = 255
		}
		if g < 30 {
			g = 30
		}
		if b > 255 {
			b = 255
		}
		if b < 20 {
			b = 20
		}
		return fmt.Sprintf("#%02x%02x%02x", r, g, b)
	case 5: // Purple/violet family (ultra_violet, space_cadet style)
		r := rand.Intn(100) + 40 // 40-139 (moderate red)
		g := rand.Intn(80) + 20  // 20-99 (lower green)
		b := rand.Intn(120) + 60 // 60-179 (blue bias for purple)
		return fmt.Sprintf("#%02x%02x%02x", r, g, b)
	default: // Rich grays with character (gunmetal, charcoal style)
		base := rand.Intn(60) + 30     // 30-89
		variation := rand.Intn(15) - 7 // Small variations
		r := base + variation
		g := base + variation + rand.Intn(10) - 5
		b := base + variation + rand.Intn(15) - 7
		if r > 120 {
			r = 120
		}
		if r < 20 {
			r = 20
		}
		if g > 120 {
			g = 120
		}
		if g < 20 {
			g = 20
		}
		if b > 120 {
			b = 120
		}
		if b < 20 {
			b = 20
		}
		return fmt.Sprintf("#%02x%02x%02x", r, g, b)
	}
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

	config := &TailwindColorConfig{}
	rand.Seed(time.Now().UnixNano())

	// 1. Separate grayscale colors from others
	var grayscaleColors []*Color
	var coloredColors []*Color

	for _, color := range palette {
		if isGrayscale(color) {
			grayscaleColors = append(grayscaleColors, color)
		} else {
			coloredColors = append(coloredColors, color)
		}
	}

	// 2. Assign primary and secondary from grayscale colors
	if len(grayscaleColors) >= 2 {
		// Sort grayscale colors by luminance
		sort.Slice(grayscaleColors, func(i, j int) bool {
			return grayscaleColors[i].Luminance() < grayscaleColors[j].Luminance()
		})

		// Use darkest and lightest grayscale colors
		config.Primary = grayscaleColors[0].Hex                        // Darkest
		config.Secondary = grayscaleColors[len(grayscaleColors)-1].Hex // Lightest
	} else if len(grayscaleColors) == 1 {
		// One grayscale color, generate another
		config.Primary = grayscaleColors[0].Hex
		if grayscaleColors[0].Luminance() < 0.5 {
			// Dark color, generate a light one
			config.Secondary = generateGrayscaleColor(0.7, 0.95)
		} else {
			// Light color, generate a dark one
			config.Secondary = generateGrayscaleColor(0.05, 0.3)
		}
	} else {
		// No grayscale colors, generate both
		config.Primary = generateGrayscaleColor(0.05, 0.3)   // Dark
		config.Secondary = generateGrayscaleColor(0.7, 0.95) // Light
	}

	// Get primary/secondary as Color objects for comparison
	primaryColor, _ := NewColor(config.Primary)
	secondaryColor, _ := NewColor(config.Secondary)

	// 3. Assign accent, neutral, and base from remaining colors (colored + unused grayscale)
	var remainingColors []*Color

	// Add unused grayscale colors (beyond the 2 used for primary/secondary)
	if len(grayscaleColors) > 2 {
		for i := 1; i < len(grayscaleColors)-1; i++ {
			remainingColors = append(remainingColors, grayscaleColors[i])
		}
	}

	// Add all colored (non-grayscale) colors
	remainingColors = append(remainingColors, coloredColors...)

	// If we don't have enough colors, generate professional ones
	for len(remainingColors) < 3 {
		colorFamily := rand.Intn(6) // 0-5 for different professional color families
		hex := generateProfessionalColor(colorFamily)
		if color, err := NewColor(hex); err == nil {
			// Check it's different enough from existing colors
			tooSimilar := false
			for _, existing := range remainingColors {
				if ColorDifference(color, existing) < 30 {
					tooSimilar = true
					break
				}
			}
			for _, existing := range grayscaleColors {
				if ColorDifference(color, existing) < 30 {
					tooSimilar = true
					break
				}
			}

			if !tooSimilar {
				remainingColors = append(remainingColors, color)
			}
		}
	}

	// Assign the remaining 3 colors (accent, neutral, base) with quality prioritization
	if len(remainingColors) >= 3 {
		// Sort by contrast to primary/secondary for better accessibility
		sort.Slice(remainingColors, func(i, j int) bool {
			distI := ColorDifference(remainingColors[i], primaryColor) + ColorDifference(remainingColors[i], secondaryColor)
			distJ := ColorDifference(remainingColors[j], primaryColor) + ColorDifference(remainingColors[j], secondaryColor)
			return distI > distJ // Sort descending by distance for better contrast
		})

		// Assign with some variation to avoid predictability
		config.Accent = remainingColors[0].Hex // Best contrast color for accent

		// For base and neutral, consider both contrast and luminance
		if len(remainingColors) >= 3 {
			// Assign based on luminance for natural hierarchy
			baseCandidate := remainingColors[1]
			neutralCandidate := remainingColors[2]

			// Base should typically be lighter (for content backgrounds)
			// Neutral should be darker (for subtle elements)
			if baseCandidate.Luminance() > neutralCandidate.Luminance() {
				config.Base = baseCandidate.Hex
				config.Neutral = neutralCandidate.Hex
			} else {
				config.Base = neutralCandidate.Hex
				config.Neutral = baseCandidate.Hex
			}
		}
	} else {
		// Fallback: generate missing colors
		if len(remainingColors) >= 1 {
			config.Accent = remainingColors[0].Hex
		} else {
			config.Accent = generateProfessionalColor(0) // Muted blue
		}

		if len(remainingColors) >= 2 {
			config.Base = remainingColors[1].Hex
		} else {
			config.Base = generateProfessionalColor(5) // Neutral gray
		}

		config.Neutral = generateProfessionalColor(1) // Earth tone
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
// Follows the principle: primary/secondary must be grayscale, others are professional colors.
func completePalette(colors []string) []string {
	fmt.Printf("--- Notice: Less than 5 colors provided. Generating %d additional colors. ---\n", 5-len(colors))

	// Seed random number generator with current time for variety
	rand.Seed(time.Now().UnixNano())

	// Create a set of existing colors to avoid duplicates
	existing := make(map[string]bool)
	for _, c := range colors {
		existing[c] = true
	}

	// Parse existing colors and categorize them
	var existingColors []*Color
	var grayscaleCount int

	for _, hex := range colors {
		if c, err := NewColor(hex); err == nil {
			existingColors = append(existingColors, c)
			if isGrayscale(c) {
				grayscaleCount++
			}
		}
	}

	// Determine what types of colors we need to add
	neededGrayscale := 2 - grayscaleCount
	if neededGrayscale < 0 {
		neededGrayscale = 0
	}

	neededProfessional := (5 - len(colors)) - neededGrayscale

	// Generate needed grayscale colors first (for primary/secondary)
	attempts := 0
	maxAttempts := 20

	for neededGrayscale > 0 && attempts < maxAttempts {
		var hex string
		if grayscaleCount == 0 {
			// Need both dark and light grayscale
			if rand.Intn(2) == 0 {
				hex = generateGrayscaleColor(0.05, 0.3) // Dark
			} else {
				hex = generateGrayscaleColor(0.7, 0.95) // Light
			}
		} else {
			// Generate complement to existing grayscale
			existingGrayscale := getAverageGrayscaleLuminance(existingColors)
			if existingGrayscale < 0.5 {
				hex = generateGrayscaleColor(0.7, 0.95) // Add light
			} else {
				hex = generateGrayscaleColor(0.05, 0.3) // Add dark
			}
		}

		if !existing[hex] {
			if newColor, err := NewColor(hex); err == nil {
				// Check it's different enough from existing
				tooSimilar := false
				for _, existing := range existingColors {
					if ColorDifference(newColor, existing) < 25 {
						tooSimilar = true
						break
					}
				}

				if !tooSimilar {
					fmt.Printf("--- Added grayscale color (%s) for primary/secondary use. ---\n", hex)
					colors = append(colors, hex)
					existing[hex] = true
					existingColors = append(existingColors, newColor)
					neededGrayscale--
					grayscaleCount++
				}
			}
		}
		attempts++
	}

	// Generate needed professional colors (for accent, neutral, base)
	colorFamilyIndex := 0
	attempts = 0

	for neededProfessional > 0 && attempts < maxAttempts {
		colorFamily := colorFamilyIndex % 7 // Cycle through 7 color families (0-6)
		hex := generateProfessionalColor(colorFamily)

		if !existing[hex] {
			if newColor, err := NewColor(hex); err == nil {
				// Check it's different enough from existing (higher threshold for more distinct colors)
				tooSimilar := false
				for _, existing := range existingColors {
					if ColorDifference(newColor, existing) < 40 {
						tooSimilar = true
						break
					}
				}

				if !tooSimilar {
					fmt.Printf("--- Added professional color (%s) for accent/neutral/base use. ---\n", hex)
					colors = append(colors, hex)
					existing[hex] = true
					existingColors = append(existingColors, newColor)
					neededProfessional--
					colorFamilyIndex++
				}
			}
		}
		attempts++
	} // Final fallback: add any remaining colors needed
	for len(colors) < 5 && attempts < maxAttempts*2 {
		var hex string
		if rand.Intn(3) == 0 {
			// Add grayscale
			hex = generateGrayscaleColor(0.2, 0.8)
		} else {
			// Add professional color
			hex = generateProfessionalColor(rand.Intn(6))
		}

		if !existing[hex] {
			fmt.Printf("--- Added fallback color (%s) to complete the palette. ---\n", hex)
			colors = append(colors, hex)
			existing[hex] = true
		}
		attempts++
	}

	return colors
}

// getAverageGrayscaleLuminance calculates the average luminance of grayscale colors
func getAverageGrayscaleLuminance(colors []*Color) float64 {
	var total float64
	var count int

	for _, color := range colors {
		if isGrayscale(color) {
			total += color.Luminance() / 255.0 // Normalize to 0-1
			count++
		}
	}

	if count == 0 {
		return 0.5 // Default middle luminance
	}

	return total / float64(count)
}
