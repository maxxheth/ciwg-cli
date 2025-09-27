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

	// 2. Randomly assign darkest/lightest to primary/secondary.
	rand.Seed(time.Now().UnixNano())
	if rand.Intn(2) == 0 {
		config.Primary = darkest.Hex
		config.Secondary = lightest.Hex
	} else {
		config.Primary = lightest.Hex
		config.Secondary = darkest.Hex
	}

	// Get primary/secondary as Color objects for comparison
	primaryColor, _ := NewColor(config.Primary)
	secondaryColor, _ := NewColor(config.Secondary)

	// 3. Assign remaining colors based on harmonic relationships (contrast).
	// Find the best accent color: the one with the highest average distance to primary and secondary.
	sort.Slice(remaining, func(i, j int) bool {
		distI := ColorDifference(remaining[i], primaryColor) + ColorDifference(remaining[i], secondaryColor)
		distJ := ColorDifference(remaining[j], primaryColor) + ColorDifference(remaining[j], secondaryColor)
		return distI > distJ // Sort descending by distance
	})

	config.Accent = remaining[0].Hex

	// Determine base and neutral from the last two colors.
	// The 'base' (background) should have high contrast with the 'primary' (text).
	rem1, rem2 := remaining[1], remaining[2]

	contrast1 := ColorDifference(rem1, primaryColor)
	contrast2 := ColorDifference(rem2, primaryColor)

	if contrast1 > contrast2 {
		config.Base = rem1.Hex
		config.Neutral = rem2.Hex
	} else {
		config.Base = rem2.Hex
		config.Neutral = rem1.Hex
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
// This is a placeholder for a more sophisticated color generation algorithm.
// Currently, it adds basic shades to reach 5 colors.
func completePalette(colors []string) []string {
	// This is a simplified approach. A more advanced version would use color theory
	// (e.g., complementary, triadic, analogous colors) to generate new colors.
	fmt.Printf("--- Notice: Less than 5 colors provided. Generating %d additional colors. ---\n", 5-len(colors))

	baseColors := map[string]string{
		"dark_gray":  "#333333",
		"gray":       "#808080",
		"light_gray": "#D3D3D3",
		"off_white":  "#F5F5F5",
	}

	// Create a set of existing colors to avoid duplicates
	existing := make(map[string]bool)
	for _, c := range colors {
		existing[c] = true
	}

	// Add new colors until we have 5
	for name, hex := range baseColors {
		if len(colors) >= 5 {
			break
		}
		if !existing[hex] {
			fmt.Printf("--- Added fallback color '%s' (%s) to complete the palette. ---\n", name, hex)
			colors = append(colors, hex)
			existing[hex] = true
		}
	}

	// If still not enough (e.g., user provided all the fallback colors), add randoms
	for len(colors) < 5 {
		hex := fmt.Sprintf("#%06x", rand.Intn(0xFFFFFF))
		if !existing[hex] {
			fmt.Printf("--- Added random color (%s) to complete the palette. ---\n", hex)
			colors = append(colors, hex)
			existing[hex] = true
		}
	}

	return colors
}
