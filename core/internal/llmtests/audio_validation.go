package llmtests

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/hajimehoshi/go-mp3"
)

// AllowedAudioFormats defines the set of valid audio formats for speech synthesis
var AllowedAudioFormats = map[string]bool{
	"flac": true, "mp3": true, "mp4": true, "mpeg": true,
	"mpga": true, "m4a": true, "ogg": true, "wav": true, "webm": true,
}

// AudioValidationResult contains the results of audio validation
type AudioValidationResult struct {
	Valid           bool
	Format          string
	MagicBytesValid bool
	DecodeValid     bool
	FileSize        int64
	Errors          []string
}

// ValidateAudioFile validates an audio file by checking magic bytes and attempting decode
func ValidateAudioFile(t *testing.T, filePath string, expectedFormat string) error {
	t.Helper()

	result := validateAudioFileInternal(filePath, expectedFormat)

	if !result.Valid {
		return fmt.Errorf("audio validation failed for %s (format: %s): %s",
			filePath, expectedFormat, strings.Join(result.Errors, "; "))
	}

	t.Logf("✅ Audio validation passed: format=%s, size=%d bytes, magic_bytes=%v, decode=%v",
		result.Format, result.FileSize, result.MagicBytesValid, result.DecodeValid)

	return nil
}

// ValidateAudioBytes validates audio bytes by checking magic bytes and attempting decode
func ValidateAudioBytes(t *testing.T, audioData []byte, expectedFormat string) error {
	t.Helper()

	result := validateAudioBytesInternal(audioData, expectedFormat)

	if !result.Valid {
		return fmt.Errorf("audio validation failed (format: %s): %s",
			expectedFormat, strings.Join(result.Errors, "; "))
	}

	t.Logf("✅ Audio validation passed: format=%s, size=%d bytes, magic_bytes=%v, decode=%v",
		result.Format, len(audioData), result.MagicBytesValid, result.DecodeValid)

	return nil
}

// SaveAndValidateAudio saves audio bytes to a temp file, validates it, and registers cleanup.
// It auto-detects the audio format from magic bytes and validates it's one of the allowed formats.
// Returns the temp file path for logging purposes.
func SaveAndValidateAudio(t *testing.T, audioData []byte) (string, error) {
	t.Helper()

	if len(audioData) == 0 {
		return "", fmt.Errorf("audio data is empty")
	}

	// Detect audio format from magic bytes
	detectedFormat := DetectAudioFormat(audioData)
	if detectedFormat == "" {
		return "", fmt.Errorf("unable to detect audio format from data (first 16 bytes: %x)", audioData[:min(16, len(audioData))])
	}

	// Validate the detected format is in the allowed list
	if !AllowedAudioFormats[detectedFormat] {
		allowedList := make([]string, 0, len(AllowedAudioFormats))
		for format := range AllowedAudioFormats {
			allowedList = append(allowedList, format)
		}
		return "", fmt.Errorf("detected format %q is not in allowed formats: %v", detectedFormat, allowedList)
	}

	// Create temp file with unique name in bifrost subdirectory
	tempDir := os.TempDir()
	bifrostDir := filepath.Join(tempDir, "bifrost")
	fileName := fmt.Sprintf("bifrost_test_speech_%s.%s", uuid.New().String(), detectedFormat)
	filePath := filepath.Join(bifrostDir, fileName)

	// Ensure bifrost subdirectory exists
	if err := os.MkdirAll(bifrostDir, 0o755); err != nil {
		return "", fmt.Errorf("failed to create temp directory: %w", err)
	}

	// Write audio data to file
	if err := os.WriteFile(filePath, audioData, 0o644); err != nil {
		return "", fmt.Errorf("failed to write audio file: %w", err)
	}

	// Register cleanup to delete file regardless of test outcome
	// t.Cleanup(func() {
	// 	if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
	// 		t.Logf("⚠️ Failed to cleanup audio file %s: %v", filePath, err)
	// 	} else {
	// 		t.Logf("🧹 Cleaned up audio file: %s", filePath)
	// 	}
	// })

	t.Logf("Detected audio format: %s, saved to temp file: %s (%d bytes)", detectedFormat, filePath, len(audioData))

	// Validate the audio file using the detected format
	if err := ValidateAudioFile(t, filePath, detectedFormat); err != nil {
		return filePath, err
	}

	return filePath, nil
}

func validateAudioFileInternal(filePath string, expectedFormat string) AudioValidationResult {
	result := AudioValidationResult{
		Format: expectedFormat,
	}

	// Read file
	data, err := os.ReadFile(filePath)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("failed to read file: %v", err))
		return result
	}

	fileInfo, err := os.Stat(filePath)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("failed to stat file: %v", err))
		return result
	}
	result.FileSize = fileInfo.Size()

	return validateAudioBytesInternal(data, expectedFormat)
}

func validateAudioBytesInternal(data []byte, expectedFormat string) AudioValidationResult {
	result := AudioValidationResult{
		Format:   expectedFormat,
		FileSize: int64(len(data)),
	}

	if len(data) == 0 {
		result.Errors = append(result.Errors, "audio data is empty")
		return result
	}

	format := strings.ToLower(expectedFormat)

	switch format {
	case "mp3":
		result.MagicBytesValid = validateMP3MagicBytes(data)
		if !result.MagicBytesValid {
			result.Errors = append(result.Errors, "invalid MP3 magic bytes")
		}

		decodeErr := validateMP3Decode(data)
		result.DecodeValid = decodeErr == nil
		if decodeErr != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("MP3 decode failed: %v", decodeErr))
		}

	case "wav":
		result.MagicBytesValid = validateWAVMagicBytes(data)
		if !result.MagicBytesValid {
			result.Errors = append(result.Errors, "invalid WAV magic bytes")
		}

		decodeErr := validateWAVDecode(data)
		result.DecodeValid = decodeErr == nil
		if decodeErr != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("WAV decode failed: %v", decodeErr))
		}

	case "flac":
		result.MagicBytesValid = validateFLACMagicBytes(data)
		if !result.MagicBytesValid {
			result.Errors = append(result.Errors, "invalid FLAC magic bytes")
		}
		// Basic magic bytes check is sufficient for FLAC
		result.DecodeValid = result.MagicBytesValid

	case "ogg", "opus":
		result.MagicBytesValid = validateOGGMagicBytes(data)
		if !result.MagicBytesValid {
			result.Errors = append(result.Errors, "invalid OGG magic bytes")
		}
		// Basic magic bytes check is sufficient for OGG
		result.DecodeValid = result.MagicBytesValid

	case "aac":
		result.MagicBytesValid = validateAACMagicBytes(data)
		if !result.MagicBytesValid {
			result.Errors = append(result.Errors, "invalid AAC magic bytes")
		}
		// Basic magic bytes check is sufficient for AAC
		result.DecodeValid = result.MagicBytesValid

	case "mp4", "m4a":
		result.MagicBytesValid = validateMP4MagicBytes(data)
		if !result.MagicBytesValid {
			result.Errors = append(result.Errors, "invalid MP4/M4A magic bytes")
		}
		// Basic magic bytes check is sufficient for MP4/M4A containers
		result.DecodeValid = result.MagicBytesValid

	case "webm":
		result.MagicBytesValid = validateWEBMMagicBytes(data)
		if !result.MagicBytesValid {
			result.Errors = append(result.Errors, "invalid WEBM magic bytes")
		}
		// Basic magic bytes check is sufficient for WEBM
		result.DecodeValid = result.MagicBytesValid

	case "mpeg", "mpga":
		// MPEG/MPGA are essentially MP3 audio
		result.MagicBytesValid = validateMP3MagicBytes(data)
		if !result.MagicBytesValid {
			result.Errors = append(result.Errors, "invalid MPEG magic bytes")
		}

		decodeErr := validateMP3Decode(data)
		result.DecodeValid = decodeErr == nil
		if decodeErr != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("MPEG decode failed: %v", decodeErr))
		}

	case "pcm16":
		// PCM has no magic bytes, just validate it has data
		result.MagicBytesValid = len(data) > 0
		result.DecodeValid = len(data) > 0 && len(data)%2 == 0 // PCM16 should have even byte count

	default:
		result.Errors = append(result.Errors, fmt.Sprintf("unsupported audio format: %s", format))
		return result
	}

	result.Valid = result.MagicBytesValid && result.DecodeValid
	return result
}

// validateMP3MagicBytes checks for valid MP3 file signatures
// MP3 files can start with:
// - ID3 tag: 0x49 0x44 0x33 ("ID3")
// - MPEG frame sync: 0xFF 0xFB, 0xFF 0xFA, 0xFF 0xF3, 0xFF 0xF2, 0xFF 0xE3, 0xFF 0xE2
func validateMP3MagicBytes(data []byte) bool {
	if len(data) < 3 {
		return false
	}

	// Check for ID3 tag
	if data[0] == 0x49 && data[1] == 0x44 && data[2] == 0x33 {
		return true
	}

	// Check for MPEG audio frame sync
	if len(data) >= 2 && data[0] == 0xFF {
		// Valid MPEG audio frame sync bytes
		// 0xFB = MPEG1 Layer3
		// 0xFA = MPEG1 Layer3 with CRC
		// 0xF3 = MPEG2 Layer3
		// 0xF2 = MPEG2 Layer3 with CRC
		// 0xE3 = MPEG2.5 Layer3
		// 0xE2 = MPEG2.5 Layer3 with CRC
		switch data[1] & 0xF6 {
		case 0xF2, 0xE2: // Layer 3
			return true
		}
		// Also check the more common patterns
		switch data[1] {
		case 0xFB, 0xFA, 0xF3, 0xF2, 0xE3, 0xE2:
			return true
		}
	}

	return false
}

// validateMP3Decode attempts to decode MP3 data to verify it's valid
func validateMP3Decode(data []byte) error {
	reader := bytes.NewReader(data)
	decoder, err := mp3.NewDecoder(reader)
	if err != nil {
		return fmt.Errorf("failed to create MP3 decoder: %w", err)
	}

	// Try to read a small sample to verify decoding works
	buf := make([]byte, 4096)
	n, err := decoder.Read(buf)
	if err != nil && err != io.EOF {
		return fmt.Errorf("failed to decode MP3 sample: %w", err)
	}

	if n == 0 && err != io.EOF {
		return fmt.Errorf("no audio data decoded from MP3")
	}

	return nil
}

// validateWAVMagicBytes checks for valid WAV file signature
// WAV files start with "RIFF" followed by file size, then "WAVE"
func validateWAVMagicBytes(data []byte) bool {
	if len(data) < 12 {
		return false
	}

	// Check RIFF header
	if string(data[0:4]) != "RIFF" {
		return false
	}

	// Check WAVE format
	if string(data[8:12]) != "WAVE" {
		return false
	}

	return true
}

// validateWAVDecode parses WAV header to verify the file structure
func validateWAVDecode(data []byte) error {
	if len(data) < 44 {
		return fmt.Errorf("WAV file too small: %d bytes (minimum 44)", len(data))
	}

	// Verify RIFF chunk
	if string(data[0:4]) != "RIFF" {
		return fmt.Errorf("missing RIFF header")
	}

	// Get file size from header (we just validate the header exists, not the size
	// since some encoders don't set this correctly and streaming may not have final size)
	_ = binary.LittleEndian.Uint32(data[4:8])

	// Verify WAVE format
	if string(data[8:12]) != "WAVE" {
		return fmt.Errorf("missing WAVE format marker")
	}

	// Find and validate fmt chunk
	offset := 12
	foundFmt := false
	foundData := false

	for offset < len(data)-8 {
		chunkID := string(data[offset : offset+4])
		chunkSize := int(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))

		if chunkID == "fmt " {
			foundFmt = true
			if offset+8+chunkSize > len(data) {
				return fmt.Errorf("fmt chunk extends beyond file")
			}

			// Validate audio format (offset+8 is start of fmt chunk data)
			if offset+10 <= len(data) {
				audioFormat := binary.LittleEndian.Uint16(data[offset+8 : offset+10])
				// 1 = PCM, 3 = IEEE float, 6 = A-law, 7 = mu-law, 0xFFFE = extensible
				validFormats := map[uint16]bool{1: true, 3: true, 6: true, 7: true, 0xFFFE: true}
				if !validFormats[audioFormat] {
					return fmt.Errorf("unsupported audio format in WAV: %d", audioFormat)
				}
			}
		}

		if chunkID == "data" {
			foundData = true
		}

		offset += 8 + chunkSize
		// Align to even boundary
		if chunkSize%2 != 0 {
			offset++
		}
	}

	if !foundFmt {
		return fmt.Errorf("missing fmt chunk in WAV file")
	}

	if !foundData {
		return fmt.Errorf("missing data chunk in WAV file")
	}

	return nil
}

// validateFLACMagicBytes checks for valid FLAC file signature
// FLAC files start with "fLaC" (0x66 0x4C 0x61 0x43)
func validateFLACMagicBytes(data []byte) bool {
	if len(data) < 4 {
		return false
	}
	return string(data[0:4]) == "fLaC"
}

// validateOGGMagicBytes checks for valid OGG file signature
// OGG files start with "OggS" (0x4F 0x67 0x67 0x53)
func validateOGGMagicBytes(data []byte) bool {
	if len(data) < 4 {
		return false
	}
	return string(data[0:4]) == "OggS"
}

// validateAACMagicBytes checks for valid AAC file signature
// AAC ADTS frames start with 0xFF 0xF1 or 0xFF 0xF9
// AAC in M4A container starts with "ftyp" at offset 4
func validateAACMagicBytes(data []byte) bool {
	if len(data) < 4 {
		return false
	}

	// Check for ADTS sync word
	if data[0] == 0xFF && (data[1]&0xF0) == 0xF0 {
		return true
	}

	// Check for M4A/MP4 container (ftyp at offset 4)
	if len(data) >= 8 && string(data[4:8]) == "ftyp" {
		return true
	}

	return false
}

// validateWEBMMagicBytes checks for valid WEBM file signature
// WEBM files start with EBML header: 0x1A 0x45 0xDF 0xA3
// and contain "webm" doctype somewhere in the first ~40 bytes
func validateWEBMMagicBytes(data []byte) bool {
	if len(data) < 4 {
		return false
	}

	// Check for EBML header (Matroska/WebM container)
	if data[0] != 0x1A || data[1] != 0x45 || data[2] != 0xDF || data[3] != 0xA3 {
		return false
	}

	// Look for "webm" doctype in the first 64 bytes
	searchLen := 64
	if len(data) < searchLen {
		searchLen = len(data)
	}

	return bytes.Contains(data[:searchLen], []byte("webm"))
}

// validateMP4MagicBytes checks for valid MP4/M4A file signature
// MP4/M4A files have "ftyp" at offset 4, followed by brand identifiers
func validateMP4MagicBytes(data []byte) bool {
	if len(data) < 12 {
		return false
	}

	// Check for ftyp box
	return string(data[4:8]) == "ftyp"
}

// DetectAudioFormat detects the audio format from the buffer header bytes.
// Returns the detected format string (mp3, wav, flac, ogg, mp4, m4a, webm) or empty string if unknown.
func DetectAudioFormat(data []byte) string {
	if len(data) < 4 {
		return ""
	}

	// Check WAV first (RIFF + WAVE)
	if validateWAVMagicBytes(data) {
		return "wav"
	}

	// Check FLAC (fLaC)
	if validateFLACMagicBytes(data) {
		return "flac"
	}

	// Check OGG/Opus (OggS)
	if validateOGGMagicBytes(data) {
		return "ogg"
	}

	// Check WEBM (EBML header with webm doctype) - check before MP4 as both are containers
	if validateWEBMMagicBytes(data) {
		return "webm"
	}

	// Check MP4/M4A container (ftyp box) - returns m4a for audio containers
	if validateMP4MagicBytes(data) {
		return "m4a"
	}

	// Check MP3 (ID3 or MPEG frame sync)
	if validateMP3MagicBytes(data) {
		return "mp3"
	}

	// Check AAC ADTS (raw AAC stream without container)
	if len(data) >= 2 && data[0] == 0xFF && (data[1]&0xF0) == 0xF0 {
		return "aac"
	}

	return ""
}
