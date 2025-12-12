// Package utils provides common utility functions used across different provider implementations.
// This file contains audio-related utility functions for format conversion.
package utils

import (
	"bytes"
	"encoding/binary"
)

// PCMConfig holds the configuration for PCM audio data
type PCMConfig struct {
	SampleRate    int // Sample rate in Hz (e.g., 24000)
	NumChannels   int // Number of audio channels (1 = mono, 2 = stereo)
	BitsPerSample int // Bits per sample (e.g., 16)
}

// DefaultGeminiPCMConfig returns the default PCM configuration for Gemini TTS
// Gemini TTS returns audio in PCM format with the following specs:
// - Format: signed 16-bit little-endian (s16le)
// - Sample rate: 24000 Hz
// - Channels: 1 (mono)
func DefaultGeminiPCMConfig() PCMConfig {
	return PCMConfig{
		SampleRate:    24000,
		NumChannels:   1,
		BitsPerSample: 16,
	}
}

// ConvertPCMToWAV converts raw PCM audio data to WAV format
// The PCM data is expected to be in signed little-endian format (s16le for 16-bit)
func ConvertPCMToWAV(pcmData []byte, config PCMConfig) ([]byte, error) {
	byteRate := config.SampleRate * config.NumChannels * config.BitsPerSample / 8
	blockAlign := config.NumChannels * config.BitsPerSample / 8

	dataSize := uint32(len(pcmData))
	fileSize := 36 + dataSize // 36 bytes for header + data

	var buf bytes.Buffer

	// RIFF header
	buf.WriteString("RIFF")
	binary.Write(&buf, binary.LittleEndian, fileSize)
	buf.WriteString("WAVE")

	// fmt subchunk
	buf.WriteString("fmt ")
	binary.Write(&buf, binary.LittleEndian, uint32(16))                   // Subchunk1Size (16 for PCM)
	binary.Write(&buf, binary.LittleEndian, uint16(1))                    // AudioFormat (1 = PCM)
	binary.Write(&buf, binary.LittleEndian, uint16(config.NumChannels))   // NumChannels
	binary.Write(&buf, binary.LittleEndian, uint32(config.SampleRate))    // SampleRate
	binary.Write(&buf, binary.LittleEndian, uint32(byteRate))             // ByteRate
	binary.Write(&buf, binary.LittleEndian, uint16(blockAlign))           // BlockAlign
	binary.Write(&buf, binary.LittleEndian, uint16(config.BitsPerSample)) // BitsPerSample

	// data subchunk
	buf.WriteString("data")
	binary.Write(&buf, binary.LittleEndian, dataSize)
	buf.Write(pcmData)

	return buf.Bytes(), nil
}

var (
	riff = []byte("RIFF")
	wave = []byte("WAVE")
	id3  = []byte("ID3")
	form = []byte("FORM")
	aiff = []byte("AIFF")
	aifc = []byte("AIFC")
	flac = []byte("fLaC")
	oggs = []byte("OggS")
	adif = []byte("ADIF")
)

// DetectAudioMimeType attempts to detect the MIME type from audio file headers.
// Supports detection of: WAV, MP3, AIFF, AAC, OGG Vorbis, and FLAC formats.
func DetectAudioMimeType(audioData []byte) string {
	if len(audioData) < 4 {
		return "audio/mp3"
	}
	// WAV (RIFF/WAVE)
	if len(audioData) >= 12 &&
		bytes.Equal(audioData[:4], riff) &&
		bytes.Equal(audioData[8:12], wave) {
		return "audio/wav"
	}
	// MP3: ID3v2 tag (keep this check for MP3)
	if len(audioData) >= 3 && bytes.Equal(audioData[:3], id3) {
		return "audio/mp3"
	}
	// AAC: ADIF or ADTS (0xFFF sync) - check before MP3 frame sync to avoid misclassification
	if bytes.HasPrefix(audioData, adif) {
		return "audio/aac"
	}
	if len(audioData) >= 2 && audioData[0] == 0xFF && (audioData[1]&0xF6) == 0xF0 {
		return "audio/aac"
	}
	// AIFF / AIFC (map both to audio/aiff)
	if len(audioData) >= 12 && bytes.Equal(audioData[:4], form) &&
		(bytes.Equal(audioData[8:12], aiff) || bytes.Equal(audioData[8:12], aifc)) {
		return "audio/aiff"
	}
	// FLAC
	if bytes.HasPrefix(audioData, flac) {
		return "audio/flac"
	}
	// OGG container
	if bytes.HasPrefix(audioData, oggs) {
		return "audio/ogg"
	}
	// MP3: MPEG frame sync (cover common variants) - check after AAC to avoid misclassification
	if len(audioData) >= 2 && audioData[0] == 0xFF &&
		(audioData[1] == 0xFB || audioData[1] == 0xF3 || audioData[1] == 0xF2 || audioData[1] == 0xFA) {
		return "audio/mp3"
	}
	// Fallback within supported set
	return "audio/mp3"
}
