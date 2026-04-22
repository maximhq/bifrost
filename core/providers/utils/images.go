package utils

import (
	"strconv"
	"strings"
)

// ConvertSizeToAspectRatioAndResolution converts a standard size string (e.g., "1024x1024")
// to an aspect ratio and image size tier.
// aspectRatio is one of "1:1", "3:4", "4:3", "9:16", "16:9" (empty if unrecognised).
// imageSize is one of "1K", "2K", "4K" (empty if out of range).
func ConvertSizeToAspectRatioAndResolution(size string) (aspectRatio, imageSize string) {
	parts := strings.Split(size, "x")
	if len(parts) != 2 {
		return "", ""
	}

	width, err1 := strconv.Atoi(parts[0])
	height, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return "", ""
	}

	if width <= 0 || height <= 0 {
		return "", ""
	}

	if width <= 1024 && height <= 1024 {
		imageSize = "1K"
	} else if width <= 2048 && height <= 2048 {
		imageSize = "2K"
	} else if width <= 4096 && height <= 4096 {
		imageSize = "4K"
	}

	ratio := float64(width) / float64(height)
	if ratio >= 0.99 && ratio <= 1.01 {
		aspectRatio = "1:1"
	} else if ratio >= 0.74 && ratio <= 0.76 {
		aspectRatio = "3:4"
	} else if ratio >= 1.32 && ratio <= 1.34 {
		aspectRatio = "4:3"
	} else if ratio >= 0.56 && ratio <= 0.57 {
		aspectRatio = "9:16"
	} else if ratio >= 1.77 && ratio <= 1.78 {
		aspectRatio = "16:9"
	}

	return aspectRatio, imageSize
}
