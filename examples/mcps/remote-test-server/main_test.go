package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"image/png"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestCaptureMediaHandler(t *testing.T) {
	for _, tc := range []struct {
		name string
		args map[string]any
		fail bool
		text string
	}{
		{name: "default", text: "capture complete"},
		{name: "success", args: map[string]any{"fail": false}, text: "capture complete"},
		{name: "nested error", args: map[string]any{"fail": true}, fail: true, text: "Error: capture media fixture failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := mcp.CallToolRequest{}
			req.Params.Arguments = tc.args
			result, err := captureMediaHandler(context.Background(), req)
			if err != nil || result == nil {
				t.Fatalf("capture_media returned result %v, error %v", result, err)
			}
			if result.IsError != tc.fail {
				t.Errorf("isError = %v, want %v", result.IsError, tc.fail)
			}
			structured, ok := result.StructuredContent.(map[string]any)
			if !ok || len(structured) != 1 || structured["title"] != "Fixture capture" {
				t.Errorf("structuredContent = %#v, want the capture title", result.StructuredContent)
			}
			if len(result.Content) != 3 {
				t.Fatalf("content = %#v, want text, image and audio", result.Content)
			}
			if text, ok := result.Content[0].(mcp.TextContent); !ok || text.Text != tc.text {
				t.Errorf("first block = %#v, want text %q", result.Content[0], tc.text)
			}
			image, ok := result.Content[1].(mcp.ImageContent)
			if !ok || image.Type != "image" || image.MIMEType != "image/png" || image.Data != fixturePNG {
				t.Errorf("second block = %#v, want the PNG fixture", result.Content[1])
			}
			audio, ok := result.Content[2].(mcp.AudioContent)
			if !ok || audio.Type != "audio" || audio.MIMEType != "audio/wav" || audio.Data != fixtureWAV {
				t.Errorf("third block = %#v, want the WAV fixture", result.Content[2])
			}
		})
	}
}

func TestCaptureMediaFixturesAreDecodable(t *testing.T) {
	imageBytes, err := base64.StdEncoding.DecodeString(fixturePNG)
	if err != nil {
		t.Fatal(err)
	}
	image, err := png.Decode(bytes.NewReader(imageBytes))
	if err != nil {
		t.Fatalf("invalid PNG fixture: %v", err)
	}
	if image.Bounds().Dx() != 1 || image.Bounds().Dy() != 1 {
		t.Errorf("PNG bounds = %v, want 1x1", image.Bounds())
	}

	wav, err := base64.StdEncoding.DecodeString(fixtureWAV)
	if err != nil {
		t.Fatal(err)
	}
	if len(wav) != 46 {
		t.Fatalf("WAV size = %d, want a 44-byte header and one 16-bit sample", len(wav))
	}
	if string(wav[:4]) != "RIFF" || binary.LittleEndian.Uint32(wav[4:8]) != uint32(len(wav)-8) || string(wav[8:16]) != "WAVEfmt " {
		t.Errorf("invalid RIFF/WAVE header: %x", wav[:16])
	}
	if binary.LittleEndian.Uint32(wav[16:20]) != 16 || binary.LittleEndian.Uint16(wav[20:22]) != 1 || binary.LittleEndian.Uint16(wav[22:24]) != 1 {
		t.Errorf("invalid mono PCM format: %x", wav[16:24])
	}
	if binary.LittleEndian.Uint32(wav[24:28]) != 8000 || binary.LittleEndian.Uint32(wav[28:32]) != 16000 {
		t.Errorf("invalid 8 kHz sample or byte rate: %x", wav[24:32])
	}
	if binary.LittleEndian.Uint16(wav[32:34]) != 2 || binary.LittleEndian.Uint16(wav[34:36]) != 16 {
		t.Errorf("invalid 16-bit block alignment: %x", wav[32:36])
	}
	if string(wav[36:40]) != "data" || binary.LittleEndian.Uint32(wav[40:44]) != 2 || binary.LittleEndian.Uint16(wav[44:46]) != 0 {
		t.Errorf("invalid silent sample: %x", wav[36:])
	}
}
