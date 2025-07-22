# 🎵 Audio Processing

Complete guide to audio processing capabilities in Bifrost including speech synthesis (text-to-speech) and audio transcription (speech-to-text) with streaming support.

> **💡 Quick Start:** Audio features are currently supported only through OpenAI models (`tts-1`, `tts-1-hd`, `whisper-1`).

---

## 📋 Overview

Bifrost provides comprehensive audio processing capabilities:

- **🔊 Speech Synthesis (TTS)**: Convert text to natural-sounding speech
- **🎤 Audio Transcription (STT)**: Convert audio files to text with timing information
- **📡 Streaming Support**: Real-time processing for both synthesis and transcription
- **🌐 API Compatibility**: Full OpenAI audio API compatibility
- **🔧 Multiple Formats**: Support for various audio formats and quality levels

**Provider Support:**

| Feature | OpenAI | Others |
|---------|--------|--------|
| Speech Synthesis | ✅ Full Support | ❌ Not Available |
| Audio Transcription | ✅ Full Support | ❌ Not Available |
| Streaming | ✅ Both Features | ❌ Not Available |

---

## 🔊 Speech Synthesis (Text-to-Speech)

Convert text to high-quality speech using AI voice models.

### **🔧 Go Package Usage**

#### **Basic Speech Synthesis**

```go
package main

import (
    "context"
    "fmt"
    "os"
    bifrost "github.com/maximhq/bifrost/core"
    "github.com/maximhq/bifrost/core/schemas"
)

func synthesizeSpeech(client *bifrost.Bifrost) error {
    text := "Welcome to Bifrost's speech synthesis feature. This text will be converted to natural-sounding speech."
    voice := "alloy"
    
    response, err := client.SpeechRequest(context.Background(), &schemas.BifrostRequest{
        Provider: schemas.OpenAI,
        Model:    "tts-1",
        Input: schemas.RequestInput{
            SpeechInput: &schemas.SpeechInput{
                Input: text,
                VoiceConfig: schemas.SpeechVoiceInput{
                    Voice: &voice,
                },
                ResponseFormat: "mp3",
            },
        },
    })
    
    if err != nil {
        return fmt.Errorf("speech synthesis failed: %v", err)
    }
    
    if response.Speech == nil {
        return fmt.Errorf("no speech data in response")
    }
    
    // Save audio to file
    filename := "synthesized_speech.mp3"
    err = os.WriteFile(filename, response.Speech.Audio, 0644)
    if err != nil {
        return fmt.Errorf("failed to save audio: %v", err)
    }
    
    fmt.Printf("Speech saved to %s (%d bytes)\n", filename, len(response.Speech.Audio))
    
    // Check usage information
    if response.Speech.Usage != nil {
        fmt.Printf("Audio duration: %.2f seconds\n", response.Speech.Usage.TotalDuration)
    }
    
    return nil
}
```

#### **Advanced Voice Configuration**

```go
func advancedSpeechSynthesis(client *bifrost.Bifrost) error {
    response, err := client.SpeechRequest(context.Background(), &schemas.BifrostRequest{
        Provider: schemas.OpenAI,
        Model:    "tts-1-hd", // Higher quality model
        Input: schemas.RequestInput{
            SpeechInput: &schemas.SpeechInput{
                Input: "The weather forecast for today shows partly cloudy skies with a high of 72 degrees.",
                VoiceConfig: schemas.SpeechVoiceInput{
                    Voice: &[]string{"nova"}[0], // Professional voice
                },
                Instructions:   "Speak slowly and clearly like a professional weather reporter.",
                ResponseFormat: "wav", // Uncompressed format
            },
        },
    })
    
    if err != nil {
        return err
    }
    
    return os.WriteFile("weather_report.wav", response.Speech.Audio, 0644)
}
```

#### **Streaming Speech Synthesis**

```go
func streamingSpeech(client *bifrost.Bifrost) error {
    longText := `
    This is a longer text that will be converted to speech using streaming.
    Streaming allows you to start playing audio while the rest is still being generated,
    providing a better user experience for longer content.
    `
    
    voice := "alloy"
    stream, err := client.SpeechStreamRequest(context.Background(), &schemas.BifrostRequest{
        Provider: schemas.OpenAI,
        Model:    "tts-1",
        Input: schemas.RequestInput{
            SpeechInput: &schemas.SpeechInput{
                Input: longText,
                VoiceConfig: schemas.SpeechVoiceInput{
                    Voice: &voice,
                },
                ResponseFormat: "mp3",
            },
        },
    })
    
    if err != nil {
        return fmt.Errorf("failed to start speech streaming: %v", err)
    }
    
    // Create output file
    audioFile, err := os.Create("streamed_speech.mp3")
    if err != nil {
        return err
    }
    defer audioFile.Close()
    
    // Process streaming chunks
    totalBytes := 0
    for chunk := range stream {
        if chunk.BifrostError != nil {
            return fmt.Errorf("stream error: %v", chunk.BifrostError)
        }
        
        if chunk.Speech != nil && chunk.Speech.Audio != nil {
            // Write audio chunk to file
            n, err := audioFile.Write(chunk.Speech.Audio)
            if err != nil {
                return fmt.Errorf("failed to write audio chunk: %v", err)
            }
            
            totalBytes += n
            fmt.Printf("Received audio chunk: %d bytes (total: %d)\n", n, totalBytes)
        }
    }
    
    fmt.Printf("Streaming complete. Total: %d bytes\n", totalBytes)
    return nil
}
```

### **🌐 HTTP API Usage**

#### **Basic Request**

```bash
curl -X POST http://localhost:8080/v1/audio/speech \
  -H "Content-Type: application/json" \
  -d '{
    "model": "openai/tts-1",
    "input": "Hello! This is a test of speech synthesis.",
    "voice": "alloy",
    "response_format": "mp3"
  }' \
  --output speech.mp3
```

#### **OpenAI Compatible Endpoint**

```bash
curl -X POST http://localhost:8080/openai/v1/audio/speech \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "tts-1",
    "input": "The quick brown fox jumps over the lazy dog.",
    "voice": "nova",
    "response_format": "wav"
  }' \
  --output speech.wav
```

#### **Python Example**

```python
import openai
import os

# Using OpenAI SDK with Bifrost
client = openai.OpenAI(
    base_url="http://localhost:8080/openai",
    api_key=os.getenv("OPENAI_API_KEY")
)

# Generate speech
response = client.audio.speech.create(
    model="tts-1",
    voice="alloy",
    input="Welcome to Bifrost's audio processing capabilities!"
)

# Save to file
with open("welcome.mp3", "wb") as f:
    f.write(response.content)

print("Speech saved to welcome.mp3")
```

### **Available Voices and Formats**

**Voices:**
- `alloy` - Neutral, balanced voice (good for general use)
- `echo` - Clear, expressive voice (good for narration)
- `fable` - Warm, narrative voice (good for storytelling)
- `onyx` - Deep, authoritative voice (good for presentations)
- `nova` - Vibrant, engaging voice (good for marketing)
- `shimmer` - Gentle, soothing voice (good for meditation)

**Audio Formats:**
- `mp3` (default) - Most compatible, smaller file size
- `opus` - Best for internet streaming
- `aac` - Good compression for mobile
- `flac` - Lossless quality (larger files)
- `wav` - Uncompressed (largest files)
- `pcm` - Raw audio data

**Models:**
- `tts-1` - Standard quality, faster generation
- `tts-1-hd` - Higher quality, slower generation

---

## 🎤 Audio Transcription (Speech-to-Text)

Convert audio files to text with advanced features like timing information and language detection.

### **🔧 Go Package Usage**

#### **Basic Transcription**

```go
func transcribeAudio(client *bifrost.Bifrost) error {
    // Read audio file
    audioData, err := os.ReadFile("speech.mp3")
    if err != nil {
        return fmt.Errorf("failed to read audio file: %v", err)
    }
    
    language := "en"
    responseFormat := "verbose_json"
    
    response, err := client.TranscriptionRequest(context.Background(), &schemas.BifrostRequest{
        Provider: schemas.OpenAI,
        Model:    "whisper-1",
        Input: schemas.RequestInput{
            TranscriptionInput: &schemas.TranscriptionInput{
                File:           audioData,
                Language:       &language,
                ResponseFormat: &responseFormat,
            },
        },
    })
    
    if err != nil {
        return fmt.Errorf("transcription failed: %v", err)
    }
    
    if response.Transcribe == nil {
        return fmt.Errorf("no transcription data in response")
    }
    
    // Display results
    fmt.Printf("Transcribed Text: %s\n", response.Transcribe.Text)
    
    if response.Transcribe.Language != nil {
        fmt.Printf("Detected Language: %s\n", *response.Transcribe.Language)
    }
    
    if response.Transcribe.Duration != nil {
        fmt.Printf("Audio Duration: %.2f seconds\n", *response.Transcribe.Duration)
    }
    
    // Word-level timing (if verbose_json format)
    if len(response.Transcribe.Words) > 0 {
        fmt.Println("\nWord-level timing:")
        for i, word := range response.Transcribe.Words {
            fmt.Printf("%d. [%.2fs-%.2fs]: %s\n", i+1, word.Start, word.End, word.Word)
            if i >= 9 { // Show first 10 words
                fmt.Printf("... (%d more words)\n", len(response.Transcribe.Words)-10)
                break
            }
        }
    }
    
    // Segment information
    if len(response.Transcribe.Segments) > 0 {
        fmt.Println("\nSegments:")
        for _, segment := range response.Transcribe.Segments {
            fmt.Printf("Segment %d [%.2fs-%.2fs]: %s\n", 
                segment.ID, segment.Start, segment.End, segment.Text)
        }
    }
    
    return nil
}
```

#### **Advanced Transcription with Context**

```go
func advancedTranscription(client *bifrost.Bifrost) error {
    audioData, err := os.ReadFile("technical_presentation.wav")
    if err != nil {
        return err
    }
    
    // Provide context for better accuracy
    prompt := "This is a recording of a technical presentation about artificial intelligence, machine learning, and software development."
    language := "en"
    responseFormat := "verbose_json"
    
    response, err := client.TranscriptionRequest(context.Background(), &schemas.BifrostRequest{
        Provider: schemas.OpenAI,
        Model:    "whisper-1",
        Input: schemas.RequestInput{
            TranscriptionInput: &schemas.TranscriptionInput{
                File:           audioData,
                Language:       &language,
                Prompt:         &prompt,
                ResponseFormat: &responseFormat,
            },
        },
        Params: &schemas.ModelParameters{
            ExtraParams: map[string]interface{}{
                "temperature": 0.0, // More deterministic output
            },
        },
    })
    
    if err != nil {
        return err
    }
    
    // Save transcription to file
    transcriptionFile := "transcription.txt"
    err = os.WriteFile(transcriptionFile, []byte(response.Transcribe.Text), 0644)
    if err != nil {
        return err
    }
    
    fmt.Printf("Transcription saved to %s\n", transcriptionFile)
    return nil
}
```

#### **Streaming Transcription**

```go
func streamingTranscription(client *bifrost.Bifrost) error {
    audioData, err := os.ReadFile("long_audio.wav")
    if err != nil {
        return err
    }
    
    stream, err := client.TranscriptionStreamRequest(context.Background(), &schemas.BifrostRequest{
        Provider: schemas.OpenAI,
        Model:    "whisper-1",
        Input: schemas.RequestInput{
            TranscriptionInput: &schemas.TranscriptionInput{
                File: audioData,
            },
        },
    })
    
    if err != nil {
        return fmt.Errorf("failed to start transcription streaming: %v", err)
    }
    
    var fullTranscription strings.Builder
    
    fmt.Println("Streaming transcription:")
    for chunk := range stream {
        if chunk.BifrostError != nil {
            return fmt.Errorf("stream error: %v", chunk.BifrostError)
        }
        
        if chunk.Transcribe != nil {
            if chunk.Transcribe.Delta != nil {
                // Streaming delta text
                delta := *chunk.Transcribe.Delta
                fmt.Print(delta)
                fullTranscription.WriteString(delta)
            } else if chunk.Transcribe.Text != "" {
                // Final complete transcription
                fmt.Printf("\n\nFinal transcription: %s\n", chunk.Transcribe.Text)
            }
        }
    }
    
    fmt.Printf("\n\nComplete transcription:\n%s\n", fullTranscription.String())
    return nil
}
```

### **🌐 HTTP API Usage**

#### **Basic Request**

```bash
curl -X POST http://localhost:8080/v1/audio/transcriptions \
  -F "model=openai/whisper-1" \
  -F "file=@audio.mp3" \
  -F "language=en" \
  -F "response_format=json"
```

#### **OpenAI Compatible Endpoint**

```bash
curl -X POST http://localhost:8080/openai/v1/audio/transcriptions \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -F "model=whisper-1" \
  -F "file=@speech.wav" \
  -F "language=en" \
  -F "response_format=verbose_json" \
  -F "temperature=0"
```

#### **Python Example**

```python
import openai
import os

client = openai.OpenAI(
    base_url="http://localhost:8080/openai",
    api_key=os.getenv("OPENAI_API_KEY")
)

# Transcribe audio file
with open("audio.mp3", "rb") as audio_file:
    transcript = client.audio.transcriptions.create(
        model="whisper-1",
        file=audio_file,
        language="en",
        response_format="verbose_json",
        temperature=0.0
    )

print(f"Transcription: {transcript.text}")
print(f"Language: {transcript.language}")
print(f"Duration: {transcript.duration}s")

# Save detailed transcription
with open("transcript.txt", "w") as f:
    f.write(transcript.text)
```

### **Supported Formats and Features**

**Input Audio Formats:**
- `mp3`, `mp4`, `mpeg`, `mpga`, `m4a`, `wav`, `webm`
- Maximum file size: 25 MB

**Response Formats:**
- `json` (default) - Basic JSON with text
- `text` - Plain text only
- `srt` - SubRip subtitle format with timing
- `verbose_json` - Detailed JSON with word/segment timing
- `vtt` - WebVTT subtitle format

**Languages:**
- 50+ supported languages with auto-detection
- Specify language code (e.g., "en", "es", "fr", "de", "ja", "zh") for better accuracy

**Features:**
- Word-level timing information
- Segment breakdown with timestamps
- Language detection
- Context prompts for improved accuracy
- Temperature control for deterministic output

---

## 🎵 Complete Audio Workflows

### **Text → Speech → Text Round-Trip**

```go
func audioRoundTrip(client *bifrost.Bifrost) error {
    ctx := context.Background()
    originalText := "The quick brown fox jumps over the lazy dog. This is a test of audio processing capabilities."
    
    fmt.Printf("Original text: %s\n", originalText)
    
    // Step 1: Convert text to speech
    fmt.Println("\n🔊 Step 1: Converting text to speech...")
    voice := "alloy"
    speechResponse, err := client.SpeechRequest(ctx, &schemas.BifrostRequest{
        Provider: schemas.OpenAI,
        Model:    "tts-1",
        Input: schemas.RequestInput{
            SpeechInput: &schemas.SpeechInput{
                Input: originalText,
                VoiceConfig: schemas.SpeechVoiceInput{
                    Voice: &voice,
                },
                ResponseFormat: "wav", // Use WAV for better transcription accuracy
            },
        },
    })
    
    if err != nil {
        return fmt.Errorf("speech synthesis failed: %v", err)
    }
    
    fmt.Printf("✅ Speech generated: %d bytes\n", len(speechResponse.Speech.Audio))
    
    // Step 2: Save audio (optional)
    tempFile := "temp_audio.wav"
    err = os.WriteFile(tempFile, speechResponse.Speech.Audio, 0644)
    if err != nil {
        return err
    }
    defer os.Remove(tempFile) // Cleanup
    
    // Step 3: Transcribe speech back to text
    fmt.Println("\n🎤 Step 2: Transcribing speech back to text...")
    transcriptionResponse, err := client.TranscriptionRequest(ctx, &schemas.BifrostRequest{
        Provider: schemas.OpenAI,
        Model:    "whisper-1",
        Input: schemas.RequestInput{
            TranscriptionInput: &schemas.TranscriptionInput{
                File: speechResponse.Speech.Audio,
            },
        },
    })
    
    if err != nil {
        return fmt.Errorf("transcription failed: %v", err)
    }
    
    transcribedText := transcriptionResponse.Transcribe.Text
    fmt.Printf("✅ Transcribed text: %s\n", transcribedText)
    
    // Step 4: Compare results
    fmt.Println("\n📊 Comparison:")
    fmt.Printf("Original:    %s\n", originalText)
    fmt.Printf("Transcribed: %s\n", transcribedText)
    
    // Calculate similarity (simple word matching)
    originalWords := strings.Fields(strings.ToLower(originalText))
    transcribedWords := strings.Fields(strings.ToLower(transcribedText))
    
    matchingWords := 0
    for _, word := range originalWords {
        for _, tWord := range transcribedWords {
            if strings.Contains(tWord, word) || strings.Contains(word, tWord) {
                matchingWords++
                break
            }
        }
    }
    
    similarity := float64(matchingWords) / float64(len(originalWords)) * 100
    fmt.Printf("Word similarity: %.1f%%\n", similarity)
    
    return nil
}
```

### **Audio Processing Pipeline**

```go
func audioProcessingPipeline(client *bifrost.Bifrost) error {
    ctx := context.Background()
    
    // Input texts for different scenarios
    texts := []struct {
        content string
        voice   string
        use     string
    }{
        {"Welcome to our customer service. How can I help you today?", "nova", "customer_service"},
        {"Breaking news: Scientists discover new exoplanet in habitable zone.", "onyx", "news"},
        {"Once upon a time, in a magical forest far away...", "fable", "storytelling"},
    }
    
    for i, text := range texts {
        fmt.Printf("\n🎬 Processing scenario %d: %s\n", i+1, text.use)
        
        // Generate speech
        speechResp, err := client.SpeechRequest(ctx, &schemas.BifrostRequest{
            Provider: schemas.OpenAI,
            Model:    "tts-1",
            Input: schemas.RequestInput{
                SpeechInput: &schemas.SpeechInput{
                    Input: text.content,
                    VoiceConfig: schemas.SpeechVoiceInput{
                        Voice: &text.voice,
                    },
                    ResponseFormat: "mp3",
                },
            },
        })
        
        if err != nil {
            fmt.Printf("❌ Speech generation failed: %v\n", err)
            continue
        }
        
        // Save audio file
        filename := fmt.Sprintf("%s.mp3", text.use)
        err = os.WriteFile(filename, speechResp.Speech.Audio, 0644)
        if err != nil {
            fmt.Printf("❌ Failed to save audio: %v\n", err)
            continue
        }
        
        fmt.Printf("✅ Audio saved: %s (%d bytes)\n", filename, len(speechResp.Speech.Audio))
        
        // Transcribe back for verification
        transcriptionResp, err := client.TranscriptionRequest(ctx, &schemas.BifrostRequest{
            Provider: schemas.OpenAI,
            Model:    "whisper-1",
            Input: schemas.RequestInput{
                TranscriptionInput: &schemas.TranscriptionInput{
                    File: speechResp.Speech.Audio,
                },
            },
        })
        
        if err != nil {
            fmt.Printf("❌ Transcription failed: %v\n", err)
            continue
        }
        
        fmt.Printf("✅ Transcription: %s\n", transcriptionResp.Transcribe.Text)
    }
    
    return nil
}
```

---

## 🔧 Best Practices

### **Performance Optimization**

1. **Choose the Right Model:**
   - Use `tts-1` for faster generation with good quality
   - Use `tts-1-hd` for higher quality when speed is less critical

2. **Audio Format Selection:**
   - Use `mp3` for most applications (good compression)
   - Use `wav` for better transcription accuracy
   - Use `opus` for real-time streaming applications

3. **Streaming for Long Content:**
   - Use streaming for texts longer than a few sentences
   - Process chunks as they arrive for better user experience

### **Quality Improvement**

1. **Voice Selection:**
   - Match voice to content type (e.g., `onyx` for professional content)
   - Test different voices for your specific use case

2. **Transcription Accuracy:**
   - Provide context prompts for domain-specific content
   - Specify language when known for better accuracy
   - Use higher quality audio formats (wav, flac)

3. **Error Handling:**
   - Always check for audio data in responses
   - Handle streaming errors gracefully
   - Implement retry logic for failed requests

### **Cost Optimization**

1. **Batch Processing:**
   - Process multiple audio files in parallel
   - Reuse client connections

2. **Format Selection:**
   - Use compressed formats (mp3, aac) to reduce bandwidth
   - Choose appropriate quality levels for your use case

---

## 🚨 Error Handling

### **Common Errors and Solutions**

```go
func handleAudioErrors(client *bifrost.Bifrost) {
    // Speech synthesis error handling
    response, err := client.SpeechRequest(ctx, request)
    if err != nil {
        switch {
        case strings.Contains(err.Error(), "unsupported operation"):
            fmt.Println("❌ Provider doesn't support audio - try OpenAI")
        case strings.Contains(err.Error(), "invalid voice"):
            fmt.Println("❌ Invalid voice selected - use: alloy, echo, fable, onyx, nova, shimmer")
        case strings.Contains(err.Error(), "text too long"):
            fmt.Println("❌ Text too long - split into smaller chunks")
        default:
            fmt.Printf("❌ Speech synthesis error: %v\n", err)
        }
        return
    }
    
    // Check for empty audio response
    if response.Speech == nil || len(response.Speech.Audio) == 0 {
        fmt.Println("❌ No audio data received")
        return
    }
    
    // Transcription error handling
    transcriptResp, err := client.TranscriptionRequest(ctx, transcriptRequest)
    if err != nil {
        switch {
        case strings.Contains(err.Error(), "file too large"):
            fmt.Println("❌ Audio file too large - maximum 25MB")
        case strings.Contains(err.Error(), "unsupported format"):
            fmt.Println("❌ Unsupported audio format - use: mp3, wav, m4a, etc.")
        case strings.Contains(err.Error(), "invalid language"):
            fmt.Println("❌ Invalid language code - use: en, es, fr, etc.")
        default:
            fmt.Printf("❌ Transcription error: %v\n", err)
        }
        return
    }
}
```

---

## 📚 Next Steps

| **Goal** | **Documentation** |
|----------|-------------------|
| **🔧 Set up Go package** | [Go Package Usage](go-package/) |
| **🌐 Use HTTP transport** | [HTTP Transport Usage](http-transport/) |
| **🔑 Configure providers** | [Providers](providers.md) |
| **❌ Handle errors** | [Error Handling](errors.md) |
| **🔌 Add custom behavior** | [Go Package Plugins](go-package/plugins.md) |

> **💡 Tip:** Audio features require OpenAI provider configuration. Other providers will return "unsupported operation" errors, but you can still use them for text-based requests in the same application. 