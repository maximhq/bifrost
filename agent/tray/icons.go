package tray

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
)

// Icon sizes for system tray.
// macOS: 22x22 (1x) and 44x44 (2x Retina)
// Linux: 22x22 common, some DEs use 16 or 24
// Windows: 16x16 to 128x128, 32x32 is common
const (
	iconSize        = 22  // Standard tray icon size
	iconSize2x      = 44  // Retina/HiDPI size
	iconViewBoxSize = 500 // Source SVG viewBox
	iconLogoScale   = 1.18
)

// --- Template Icons (macOS) ---
// Template icons use only black + transparent pixels.
// macOS automatically adapts them to light/dark menu bar themes.

var (
	// TemplateIconConnected is a monochrome pixel-style Bifrost mark for macOS.
	// Used with systray.SetTemplateIcon() for automatic light/dark menu bar support.
	TemplateIconConnected = generateBifrostTemplate(iconSize2x, 1.0)

	// TemplateIconDisconnected is a dimmed version (lower alpha) for disabled state.
	TemplateIconDisconnected = generateBifrostTemplate(iconSize2x, 0.4)
)

// --- Color Icons (Linux/Windows) ---
// Color icons are used on platforms that don't support template images.

var (
	// IconConnected renders the same logo silhouette in the active color.
	IconConnected = generateBifrostColor(iconSize2x, color.RGBA{R: 76, G: 175, B: 80, A: 255})

	// IconDisconnected keeps the same logo silhouette in a muted color.
	IconDisconnected = generateBifrostColor(iconSize2x, color.RGBA{R: 158, G: 158, B: 158, A: 255})

	// IconError uses the same logo silhouette in red for visibility.
	IconError = generateBifrostColor(iconSize2x, color.RGBA{R: 244, G: 67, B: 54, A: 255})

	// IconWarning uses the same logo silhouette in amber for visibility.
	IconWarning = generateBifrostColor(iconSize2x, color.RGBA{R: 255, G: 193, B: 7, A: 255})

	// ConnectingAnimationFrames is a set of template icon frames that pulse
	// in opacity for a "connecting" animation. 6 frames at 200ms each = 1.2s cycle.
	// The opacity follows a sine curve: dim → bright → dim.
	ConnectingAnimationFrames = generateAnimationFrames(iconSize2x, 6)
)

type iconRect struct {
	x float64
	y float64
	w float64
	h float64
}

type iconPoint struct {
	x float64
	y float64
}

var (
	// These rectangles match the logo structure the user shared, scaled from a
	// 500x500 artboard into the tray icon.
	baseBlockRects = []iconRect{
		{x: 139, y: 65, w: 222, h: 74},
		{x: 139, y: 361, w: 222, h: 74},
		{x: 361, y: 139, w: 74, h: 74},
		{x: 361, y: 288, w: 74, h: 74},
		{x: 213, y: 213, w: 74, h: 74},
	}
	blockRects = scaleRects(baseBlockRects, blockRectBoundsCenter(baseBlockRects), iconLogoScale)
)

// generateBifrostTemplate creates the monochrome template icon used by macOS.
// Template icons render correctly in both dark and light menu bar modes.
func generateBifrostTemplate(size int, alphaScale float64) []byte {
	return renderBifrostIcon(size, color.RGBA{R: 0, G: 0, B: 0, A: 255}, alphaScale)
}

// generateBifrostColor creates the colored tray icon for Linux/Windows.
func generateBifrostColor(size int, fillColor color.RGBA) []byte {
	return renderBifrostIcon(size, fillColor, 1.0)
}

func renderBifrostIcon(size int, fillColor color.RGBA, alphaScale float64) []byte {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	scale := float64(iconViewBoxSize) / float64(size)
	feather := scale * 0.45

	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			fx := (float64(x) + 0.5) * scale
			fy := (float64(y) + 0.5) * scale

			alpha := 0.0
			for _, rect := range blockRects {
				alpha = math.Max(alpha, rectCoverage(fx, fy, rect, feather))
			}
			if alpha > 0 {
				img.SetRGBA(x, y, applyAlpha(fillColor, alpha*alphaScale))
			}
		}
	}

	return encodePNG(img)
}

func rectCoverage(px float64, py float64, rect iconRect, feather float64) float64 {
	if px >= rect.x && px <= rect.x+rect.w && py >= rect.y && py <= rect.y+rect.h {
		return 1
	}

	dx := math.Max(math.Max(rect.x-px, 0), px-(rect.x+rect.w))
	dy := math.Max(math.Max(rect.y-py, 0), py-(rect.y+rect.h))
	dist := math.Hypot(dx, dy)
	if dist >= feather {
		return 0
	}
	return 1 - dist/feather
}

func scaleRects(rects []iconRect, center iconPoint, scale float64) []iconRect {
	scaled := make([]iconRect, len(rects))
	for i, rect := range rects {
		scaled[i] = iconRect{
			x: center.x + (rect.x-center.x)*scale,
			y: center.y + (rect.y-center.y)*scale,
			w: rect.w * scale,
			h: rect.h * scale,
		}
	}
	return scaled
}

func blockRectBoundsCenter(rects []iconRect) iconPoint {
	if len(rects) == 0 {
		return iconPoint{x: iconViewBoxSize / 2, y: iconViewBoxSize / 2}
	}

	minX := rects[0].x
	minY := rects[0].y
	maxX := rects[0].x + rects[0].w
	maxY := rects[0].y + rects[0].h

	for _, rect := range rects[1:] {
		minX = math.Min(minX, rect.x)
		minY = math.Min(minY, rect.y)
		maxX = math.Max(maxX, rect.x+rect.w)
		maxY = math.Max(maxY, rect.y+rect.h)
	}

	return iconPoint{x: (minX + maxX) / 2, y: (minY + maxY) / 2}
}

func applyAlpha(c color.RGBA, alpha float64) color.RGBA {
	return color.RGBA{
		R: c.R,
		G: c.G,
		B: c.B,
		A: uint8(clamp(alpha*float64(c.A), 0, 255)),
	}
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func encodePNG(img image.Image) []byte {
	var buf bytes.Buffer
	png.Encode(&buf, img)
	return buf.Bytes()
}

// generateAnimationFrames creates a series of template icon frames with a
// sinusoidal opacity pulse for the "connecting" animation.
// The alpha follows a sine wave: 0.3 → 1.0 → 0.3 over numFrames steps.
func generateAnimationFrames(size int, numFrames int) [][]byte {
	frames := make([][]byte, numFrames)
	for i := 0; i < numFrames; i++ {
		// Sine wave: maps frame index to 0.3..1.0 range
		t := float64(i) / float64(numFrames)
		alpha := 0.3 + 0.7*math.Pow(math.Sin(t*math.Pi), 2)
		frames[i] = generateBifrostTemplate(size, alpha)
	}
	return frames
}
