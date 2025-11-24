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
