package live

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/tidwall/gjson"
)

// Speech for the real upstream: macOS `say` and `afconvert` turn a prompt into what a
// microphone would send. Tests that need speech skip where those tools are missing.

type gjsonResult = gjson.Result

func requireSpeechTools(t *testing.T) bool {
	t.Helper()
	for _, tool := range []string{"say", "afconvert"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not available; spoken scenarios need macOS speech tools", tool)
			return false
		}
	}
	return true
}

// speakPCM renders text as 24 kHz mono PCM16, the format a WebSocket session declares.
func speakPCM(t *testing.T, text string) []byte {
	t.Helper()
	requireSpeechTools(t)
	dir := t.TempDir()
	aiff := filepath.Join(dir, "p.aiff")
	wav := filepath.Join(dir, "p.wav")
	if out, err := exec.Command("say", "-o", aiff, text).CombinedOutput(); err != nil {
		t.Fatalf("say: %v %s", err, out)
	}
	if out, err := exec.Command("afconvert", "-f", "WAVE", "-d", "LEI16@24000", "-c", "1", aiff, wav).CombinedOutput(); err != nil {
		t.Fatalf("afconvert: %v %s", err, out)
	}
	pcm, err := readWAVData(wav)
	if err != nil {
		t.Fatal(err)
	}
	return pcm
}

// speakOpus renders text as 20 ms Opus packets for the WebRTC microphone track. It returns
// false where afconvert cannot encode Opus.
func speakOpus(t *testing.T, text string) ([][]byte, bool) {
	t.Helper()
	if !requireSpeechTools(t) {
		return nil, false
	}
	dir := t.TempDir()
	aiff := filepath.Join(dir, "p.aiff")
	wav := filepath.Join(dir, "p.wav")
	caf := filepath.Join(dir, "p.caf")
	if out, err := exec.Command("say", "-o", aiff, text).CombinedOutput(); err != nil {
		t.Fatalf("say: %v %s", err, out)
	}
	if out, err := exec.Command("afconvert", "-f", "WAVE", "-d", "LEI16@48000", "-c", "1", aiff, wav).CombinedOutput(); err != nil {
		t.Fatalf("afconvert to 48k wav: %v %s", err, out)
	}
	if _, err := exec.Command("afconvert", "-f", "caff", "-d", "opus", wav, caf).CombinedOutput(); err != nil {
		return nil, false
	}
	packets, err := readCAFOpusPackets(caf)
	if err != nil {
		t.Fatal(err)
	}
	return packets, true
}

func readWAVData(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	for i := 12; i+8 <= len(data); {
		id := string(data[i : i+4])
		size := int(binary.LittleEndian.Uint32(data[i+4 : i+8]))
		if id == "data" {
			return data[i+8 : min(i+8+size, len(data))], nil
		}
		i += 8 + size + size%2
	}
	return nil, errors.New("no data chunk in " + path)
}

// readCAFOpusPackets splits a CAF Opus file into its 20 ms packets by its packet table.
func readCAFOpusPackets(path string) ([][]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) < 8 || string(data[:4]) != "caff" {
		return nil, errors.New("not a CAF file: " + path)
	}
	var sizes []int
	var audio []byte
	for i := 8; i+12 <= len(data); {
		chunkType := string(data[i : i+4])
		size := int(binary.BigEndian.Uint64(data[i+4 : i+12]))
		body := data[i+12 : min(i+12+size, len(data))]
		switch chunkType {
		case "desc":
			if fpp := binary.BigEndian.Uint32(body[20:24]); fpp != 960 {
				return nil, fmt.Errorf("%s: %d frames per packet, want 960", path, fpp)
			}
		case "pakt":
			packets := int(binary.BigEndian.Uint64(body[:8]))
			pos := 24
			for range packets {
				value := 0
				for {
					b := body[pos]
					pos++
					value = value<<7 | int(b&0x7f)
					if b < 0x80 {
						break
					}
				}
				sizes = append(sizes, value)
			}
		case "data":
			audio = body[4:]
		}
		i += 12 + size
	}
	var packets [][]byte
	for _, size := range sizes {
		if size > len(audio) {
			return nil, errors.New("packet table overruns audio data in " + path)
		}
		packets = append(packets, audio[:size])
		audio = audio[size:]
	}
	if len(packets) == 0 {
		return nil, errors.New("no opus packets in " + path)
	}
	return packets, nil
}
