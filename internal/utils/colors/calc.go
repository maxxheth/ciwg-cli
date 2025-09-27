package colors

import (
	"encoding/hex"
	"fmt"
	"math"
)

// Color represents a color with its different model representations.
type Color struct {
	Hex string
	RGB RGB
	LAB LAB
}

// RGB represents the Red, Green, Blue color model.
type RGB struct {
	R, G, B uint8
}

// LAB represents the CIE L*a*b* color space, which is useful for calculating perceptual color differences.
type LAB struct {
	L, A, B float64
}

// NewColor creates a new Color object from a hex string.
// It automatically parses the hex string and converts it to RGB and LAB color spaces.
func NewColor(hexStr string) (*Color, error) {
	rgb, err := hexToRGB(hexStr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse hex color '%s': %w", hexStr, err)
	}

	lab := rgbToLAB(rgb)

	return &Color{
		Hex: hexStr,
		RGB: rgb,
		LAB: lab,
	}, nil
}

// Luminance calculates the perceived brightness of the color.
// The values range from 0 (darkest) to 255 (lightest).
func (c *Color) Luminance() float64 {
	return 0.299*float64(c.RGB.R) + 0.587*float64(c.RGB.G) + 0.114*float64(c.RGB.B)
}

// hexToRGB converts a hex color string (e.g., "#RRGGBB") to an RGB struct.
func hexToRGB(h string) (RGB, error) {
	if h[0] == '#' {
		h = h[1:]
	}
	if len(h) != 6 {
		return RGB{}, fmt.Errorf("invalid hex code length")
	}
	bytes, err := hex.DecodeString(h)
	if err != nil {
		return RGB{}, err
	}
	return RGB{R: bytes[0], G: bytes[1], B: bytes[2]}, nil
}

// rgbToLAB converts an RGB color to the CIE L*a*b* color space.
// This is a two-step process: RGB -> XYZ -> L*a*b*.
func rgbToLAB(rgb RGB) LAB {
	// Normalize R, G, B to 0-1
	r := float64(rgb.R) / 255.0
	g := float64(rgb.G) / 255.0
	b := float64(rgb.B) / 255.0

	// Apply gamma correction (sRGB to linear)
	if r > 0.04045 {
		r = math.Pow((r+0.055)/1.055, 2.4)
	} else {
		r = r / 12.92
	}
	if g > 0.04045 {
		g = math.Pow((g+0.055)/1.055, 2.4)
	} else {
		g = g / 12.92
	}
	if b > 0.04045 {
		b = math.Pow((b+0.055)/1.055, 2.4)
	} else {
		b = b / 12.92
	}

	// Convert to XYZ color space
	x := r*0.4124564 + g*0.3575761 + b*0.1804375
	y := r*0.2126729 + g*0.7151522 + b*0.0721750
	z := r*0.0193339 + g*0.1191920 + b*0.9503041

	// Reference white point (D65 illuminant)
	refX := 0.95047
	refY := 1.00000
	refZ := 1.08883

	x /= refX
	y /= refY
	z /= refZ

	// Convert XYZ to L*a*b*
	if x > 0.008856 {
		x = math.Pow(x, 1.0/3.0)
	} else {
		x = (7.787 * x) + (16.0 / 116.0)
	}
	if y > 0.008856 {
		y = math.Pow(y, 1.0/3.0)
	} else {
		y = (7.787 * y) + (16.0 / 116.0)
	}
	if z > 0.008856 {
		z = math.Pow(z, 1.0/3.0)
	} else {
		z = (7.787 * z) + (16.0 / 116.0)
	}

	l := (116.0 * y) - 16.0
	a := 500.0 * (x - y)
	bVal := 200.0 * (y - z)

	return LAB{L: l, A: a, B: bVal}
}

// ColorDifference calculates the perceptual difference between two colors using the CIE76 formula.
// A higher value means a greater, more noticeable difference.
func ColorDifference(c1, c2 *Color) float64 {
	deltaL := c1.LAB.L - c2.LAB.L
	deltaA := c1.LAB.A - c2.LAB.A
	deltaB := c1.LAB.B - c2.LAB.B
	return math.Sqrt(deltaL*deltaL + deltaA*deltaA + deltaB*deltaB)
}

// getTextColorForBackground determines if a background color needs light or dark text for contrast.
func getTextColorForBackground(hexBg string) string {
	c, err := NewColor(hexBg)
	if err != nil {
		// Default to black on error
		return "#000000"
	}
	// A luminance of 128 is a common threshold. Higher is lighter.
	if c.Luminance() > 128 {
		return "#000000" // Dark text for a light background
	}
	return "#ffffff" // Light text for a dark background
}
