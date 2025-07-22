# 🤖 Bifrost Client

Complete guide to using the main Bifrost client methods for chat completions, text completions, and request handling patterns.

> **💡 Quick Start:** See the [30-second setup](../../quickstart/go-package.md) to get a client running quickly.

---

## 📋 Client Overview

The Bifrost client is your main interface for making AI requests. It handles:

- **Request routing** to appropriate providers
- **Automatic fallbacks** when providers fail
- **Concurrent processing** with worker pools
- **Plugin execution** for custom middleware
- **MCP tool integration** for external capabilities

```go
// Initialize client
client, initErr := bifrost.Init(schemas.BifrostConfig{
    Account: &MyAccount{},
})
defer client.Cleanup() // Always cleanup!

// Make requests
response, bifrostErr := client.ChatCompletionRequest(ctx, request)
```

---

## 🚀 Core Methods

### **Chat Completion**

The primary method for conversational AI interactions:

```go
func (b *Bifrost) ChatCompletionRequest(
    ctx context.Context,
    req *schemas.BifrostRequest
) (*schemas.BifrostResponse, *schemas.BifrostError)
```

**Basic Example:**

```go
message := "Explain quantum computing in simple terms"
response, err := client.ChatCompletionRequest(context.Background(), &schemas.BifrostRequest{
    Provider: schemas.OpenAI,
    Model:    "gpt-4o-mini",
    Input: schemas.RequestInput{
        ChatCompletionInput: &[]schemas.BifrostMessage{
            {
                Role: schemas.ModelChatMessageRoleUser,
                Content: schemas.MessageContent{ContentStr: &message},
            },
        },
    },
})

if err != nil {
    log.Printf("Request failed: %v", err)
    return
}

// Access response
if len(response.Choices) > 0 && response.Choices[0].Message.Content.ContentStr != nil {
    fmt.Println("AI Response:", *response.Choices[0].Message.Content.ContentStr)
}
```

### **Chat Completion Streaming**

Stream chat responses for real-time user experience:

```go
func (b *Bifrost) ChatCompletionStreamRequest(
    ctx context.Context,
    req *schemas.BifrostRequest
) (chan *schemas.BifrostStream, *schemas.BifrostError)
```

**Basic Streaming Example:**

```go
message := "Write a short story about a robot learning to paint"
stream, err := client.ChatCompletionStreamRequest(context.Background(), &schemas.BifrostRequest{
    Provider: schemas.OpenAI,
    Model:    "gpt-4o-mini",
    Input: schemas.RequestInput{
        ChatCompletionInput: &[]schemas.BifrostMessage{
            {
                Role: schemas.ModelChatMessageRoleUser,
                Content: schemas.MessageContent{ContentStr: &message},
            },
        },
    },
})

if err != nil {
    log.Printf("Streaming request failed: %v", err)
    return
}

// Process streaming response
var fullResponse strings.Builder
fmt.Print("AI Response: ")

for chunk := range stream {
    // Handle errors in stream
    if chunk.BifrostError != nil {
        log.Printf("Stream error: %v", chunk.BifrostError)
        break
    }
    
    // Process response chunks
    if len(chunk.Choices) > 0 {
        choice := chunk.Choices[0]
        
        // Check for streaming content
        if choice.BifrostStreamResponseChoice != nil && 
           choice.BifrostStreamResponseChoice.Delta.Content != nil {
            
            content := *choice.BifrostStreamResponseChoice.Delta.Content
            fmt.Print(content) // Print content as it arrives
            fullResponse.WriteString(content)
        }
        
        // Check for finish reason
        if choice.FinishReason != nil {
            fmt.Printf("\n\nStream finished: %s\n", *choice.FinishReason)
            break
        }
    }
}

fmt.Printf("\n\nComplete response:\n%s\n", fullResponse.String())
```

**Advanced Streaming with Conversation History:**

```go
func streamingConversation(client *bifrost.Bifrost) error {
    conversation := []schemas.BifrostMessage{
        {
            Role: schemas.ModelChatMessageRoleSystem,
            Content: schemas.MessageContent{
                ContentStr: &[]string{"You are a helpful assistant that explains complex topics simply."}[0],
            },
        },
        {
            Role: schemas.ModelChatMessageRoleUser,
            Content: schemas.MessageContent{
                ContentStr: &[]string{"Explain quantum computing in simple terms"}[0],
            },
        },
    }
    
    stream, err := client.ChatCompletionStreamRequest(context.Background(), &schemas.BifrostRequest{
        Provider: schemas.Anthropic,
        Model:    "claude-3-sonnet-20240229",
        Input: schemas.RequestInput{
            ChatCompletionInput: &conversation,
        },
        Params: &schemas.ModelParameters{
            Temperature: &[]float64{0.7}[0],
            MaxTokens:   &[]int{1000}[0],
        },
    })
    
    if err != nil {
        return fmt.Errorf("failed to start streaming: %v", err)
    }
    
    var responseBuilder strings.Builder
    fmt.Println("🤖 Assistant: ")
    
    for chunk := range stream {
        if chunk.BifrostError != nil {
            return fmt.Errorf("stream error: %v", chunk.BifrostError)
        }
        
        if len(chunk.Choices) > 0 && chunk.Choices[0].BifrostStreamResponseChoice != nil {
            delta := chunk.Choices[0].BifrostStreamResponseChoice.Delta
            
            // Handle role (usually only in first chunk)
            if delta.Role != nil {
                fmt.Printf("[Role: %s] ", *delta.Role)
            }
            
            // Handle content
            if delta.Content != nil {
                content := *delta.Content
                fmt.Print(content)
                responseBuilder.WriteString(content)
            }
            
            // Handle tool calls (if any)
            if len(delta.ToolCalls) > 0 {
                fmt.Printf("\n[Tool calls detected: %d]\n", len(delta.ToolCalls))
                for _, toolCall := range delta.ToolCalls {
                    fmt.Printf("Tool: %s\n", toolCall.Function.Name)
                }
            }
            
            // Handle finish reason
            if chunk.Choices[0].FinishReason != nil {
                finishReason := *chunk.Choices[0].FinishReason
                fmt.Printf("\n\n[Finished: %s]\n", finishReason)
                
                if finishReason == "tool_calls" {
                    fmt.Println("Note: Response ended with tool calls - you may need to handle tool execution")
                }
                break
            }
        }
    }
    
    finalResponse := responseBuilder.String()
    fmt.Printf("\n\nFinal response length: %d characters\n", len(finalResponse))
    
    return nil
}
```

**Streaming with Error Recovery:**

```go
func robustStreaming(client *bifrost.Bifrost) error {
    ctx := context.Background()
    message := "Tell me about the latest developments in artificial intelligence"
    
    // Create request with fallbacks
    request := &schemas.BifrostRequest{
        Provider: schemas.OpenAI,
        Model:    "gpt-4o-mini",
        Input: schemas.RequestInput{
            ChatCompletionInput: &[]schemas.BifrostMessage{
                {
                    Role: schemas.ModelChatMessageRoleUser,
                    Content: schemas.MessageContent{ContentStr: &message},
                },
            },
        },
        Fallbacks: []schemas.Fallback{
            {Provider: schemas.Anthropic, Model: "claude-3-sonnet-20240229"},
            {Provider: schemas.Vertex, Model: "gemini-pro"},
        },
    }
    
    stream, err := client.ChatCompletionStreamRequest(ctx, request)
    if err != nil {
        return fmt.Errorf("failed to start streaming: %v", err)
    }
    
    var (
        responseBuilder strings.Builder
        chunkCount      int
        errorCount      int
    )
    
    fmt.Println("🚀 Starting streaming response...")
    
    for chunk := range stream {
        chunkCount++
        
        // Handle stream errors
        if chunk.BifrostError != nil {
            errorCount++
            fmt.Printf("\n❌ Stream error %d: %v\n", errorCount, chunk.BifrostError)
            
            // Continue processing unless too many errors
            if errorCount > 3 {
                return fmt.Errorf("too many stream errors (%d), aborting", errorCount)
            }
            continue
        }
        
        // Process successful chunks
        if len(chunk.Choices) > 0 && chunk.Choices[0].BifrostStreamResponseChoice != nil {
            delta := chunk.Choices[0].BifrostStreamResponseChoice.Delta
            
            if delta.Content != nil {
                content := *delta.Content
                fmt.Print(content)
                responseBuilder.WriteString(content)
            }
            
            // Check for completion
            if chunk.Choices[0].FinishReason != nil {
                fmt.Printf("\n\n✅ Stream completed successfully!")
                fmt.Printf("\nProvider used: %s", chunk.ExtraFields.Provider)
                fmt.Printf("\nChunks processed: %d", chunkCount)
                fmt.Printf("\nErrors encountered: %d", errorCount)
                break
            }
        }
    }
    
    finalResponse := responseBuilder.String()
    if len(finalResponse) == 0 {
        return fmt.Errorf("no content received from stream")
    }
    
    fmt.Printf("\n\nResponse summary:")
    fmt.Printf("\n- Length: %d characters", len(finalResponse))
    fmt.Printf("\n- Words: ~%d", len(strings.Fields(finalResponse)))
    fmt.Printf("\n- Chunks: %d", chunkCount)
    
    return nil
}
```

### **Text Completion**

For simple text generation without conversation context:

```go
func (b *Bifrost) TextCompletionRequest(
    ctx context.Context,
    req *schemas.BifrostRequest
) (*schemas.BifrostResponse, *schemas.BifrostError)
```

**Basic Example:**

```go
prompt := "Complete this story: Once upon a time in a digital realm,"
response, bifrostErr := client.TextCompletionRequest(context.Background(), &schemas.BifrostRequest{
    Provider: schemas.Anthropic,
    Model:    "claude-2.1", // Text completion models
        Input: schemas.RequestInput{
            TextCompletionInput: &prompt,
        },
        Params: &schemas.ModelParameters{
            MaxTokens: bifrost.Ptr(100),
        },
})
```

### **MCP Tool Execution**

Execute external tools manually for security and control:

```go
func (b *Bifrost) ExecuteMCPTool(
    ctx context.Context,
    toolCall schemas.ToolCall
) (*schemas.BifrostMessage, *schemas.BifrostError)
```

> **📖 Learn More:** See [MCP Integration](./mcp.md) for complete tool setup and usage patterns.

### **🔊 Speech Synthesis (Text-to-Speech)**

Convert text to spoken audio using AI voice synthesis:

```go
func (b *Bifrost) SpeechRequest(
    ctx context.Context,
    req *schemas.BifrostRequest
) (*schemas.BifrostResponse, *schemas.BifrostError)
```

**Basic Example:**

```go
text := "Hello! Welcome to Bifrost's speech synthesis feature."
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
    log.Printf("Speech synthesis failed: %v", err)
    return
}

if response.Speech != nil {
    // Save audio to file
    err = os.WriteFile("speech.mp3", response.Speech.Audio, 0644)
    if err != nil {
        log.Printf("Failed to save audio: %v", err)
        return
    }
    
    fmt.Println("Speech saved to speech.mp3")
    
    // Check usage stats
    if response.Speech.Usage != nil {
        fmt.Printf("Audio duration: %.2f seconds\n", response.Speech.Usage.TotalDuration)
    }
}
```

**Advanced Voice Configuration:**

```go
// Different voice and format
response, err := client.SpeechRequest(ctx, &schemas.BifrostRequest{
    Provider: schemas.OpenAI,
    Model:    "tts-1-hd", // Higher quality model
    Input: schemas.RequestInput{
        SpeechInput: &schemas.SpeechInput{
            Input: "The weather today is sunny with light rain expected.",
            VoiceConfig: schemas.SpeechVoiceInput{
                Voice: &[]string{"nova"}[0], // Different voice
            },
            Instructions:   "Speak slowly and clearly like a weather reporter.",
            ResponseFormat: "wav", // Different audio format
        },
    },
})
```

**Available Voices (OpenAI):**
- `alloy` - Neutral, balanced voice
- `echo` - Clear, expressive voice
- `fable` - Warm, narrative voice  
- `onyx` - Deep, authoritative voice
- `nova` - Vibrant, engaging voice
- `shimmer` - Gentle, soothing voice

**Audio Formats:**
- `mp3` (default) - Most compatible
- `opus` - Internet streaming
- `aac` - Digital compression
- `flac` - Lossless audio
- `wav` - Uncompressed
- `pcm` - Raw audio data

### **🔊 Speech Streaming**

Stream audio synthesis for longer texts or real-time applications:

```go
func (b *Bifrost) SpeechStreamRequest(
    ctx context.Context,
    req *schemas.BifrostRequest
) (chan *schemas.BifrostStream, *schemas.BifrostError)
```

**Streaming Example:**

```go
longText := "This is a longer text that will be streamed as audio chunks..."
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
    log.Printf("Speech streaming failed: %v", err)
    return
}

// Create file to write audio chunks
audioFile, err := os.Create("streamed_speech.mp3")
if err != nil {
    log.Fatal(err)
}
defer audioFile.Close()

// Process streaming audio chunks
for chunk := range stream {
    if chunk.BifrostError != nil {
        log.Printf("Stream error: %v", chunk.BifrostError)
        break
    }
    
    if chunk.Speech != nil && chunk.Speech.Audio != nil {
        // Write audio chunk to file
        _, err := audioFile.Write(chunk.Speech.Audio)
        if err != nil {
            log.Printf("Failed to write audio chunk: %v", err)
            break
        }
        
        fmt.Printf("Received audio chunk: %d bytes\n", len(chunk.Speech.Audio))
    }
}

fmt.Println("Streaming complete")
```

### **🎤 Audio Transcription (Speech-to-Text)**

Convert audio files to text using AI transcription:

```go
func (b *Bifrost) TranscriptionRequest(
    ctx context.Context,
    req *schemas.BifrostRequest
) (*schemas.BifrostResponse, *schemas.BifrostError)
```

**Basic Example:**

```go
// Read audio file
audioData, err := os.ReadFile("speech.mp3")
if err != nil {
    log.Fatal(err)
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
    log.Printf("Transcription failed: %v", err)
    return
}

if response.Transcribe != nil {
    // Get transcribed text
    fmt.Printf("Transcription: %s\n", response.Transcribe.Text)
    
    // Check detected language
    if response.Transcribe.Language != nil {
        fmt.Printf("Detected language: %s\n", *response.Transcribe.Language)
    }
    
    // Check audio duration
    if response.Transcribe.Duration != nil {
        fmt.Printf("Audio duration: %.2f seconds\n", *response.Transcribe.Duration)
    }
    
    // Process word-level timing (if verbose_json format)
    if len(response.Transcribe.Words) > 0 {
        fmt.Println("\nWord-level timing:")
        for _, word := range response.Transcribe.Words {
            fmt.Printf("  [%.2fs-%.2fs]: %s\n", word.Start, word.End, word.Word)
        }
    }
    
    // Process segments
    if len(response.Transcribe.Segments) > 0 {
        fmt.Println("\nSegments:")
        for _, segment := range response.Transcribe.Segments {
            fmt.Printf("  Segment %d [%.2fs-%.2fs]: %s\n", 
                segment.ID, segment.Start, segment.End, segment.Text)
        }
    }
}
```

**Advanced Transcription:**

```go
// With context prompt for better accuracy
prompt := "This is a recording of a technical presentation about AI and machine learning."

response, err := client.TranscriptionRequest(ctx, &schemas.BifrostRequest{
    Provider: schemas.OpenAI,
    Model:    "whisper-1",
    Input: schemas.RequestInput{
        TranscriptionInput: &schemas.TranscriptionInput{
            File:           audioData,
            Language:       &language,
            Prompt:         &prompt, // Context for better accuracy
            ResponseFormat: &responseFormat,
        },
    },
    Params: &schemas.ModelParameters{
        ExtraParams: map[string]interface{}{
            "temperature": 0.0, // More deterministic output
        },
    },
})
```

**Response Formats:**
- `json` (default) - Basic JSON with text
- `text` - Plain text only
- `srt` - SubRip subtitle format
- `verbose_json` - Detailed JSON with timing
- `vtt` - WebVTT subtitle format

**Supported Audio Formats:**
- `mp3`, `mp4`, `mpeg`, `mpga`, `m4a`, `wav`, `webm`
- Maximum file size: 25 MB

### **🎤 Transcription Streaming**

Stream transcription for real-time applications:

```go
func (b *Bifrost) TranscriptionStreamRequest(
    ctx context.Context,
    req *schemas.BifrostRequest
) (chan *schemas.BifrostStream, *schemas.BifrostError)
```

**Streaming Example:**

```go
// Read audio file
audioData, err := os.ReadFile("long_audio.wav")
if err != nil {
    log.Fatal(err)
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
    log.Printf("Transcription streaming failed: %v", err)
    return
}

var fullTranscription strings.Builder

// Process streaming transcription chunks
for chunk := range stream {
    if chunk.BifrostError != nil {
        log.Printf("Stream error: %v", chunk.BifrostError)
        break
    }
    
    if chunk.Transcribe != nil {
        if chunk.Transcribe.Delta != nil {
            // Streaming delta text
            fmt.Printf("Delta: %s", *chunk.Transcribe.Delta)
            fullTranscription.WriteString(*chunk.Transcribe.Delta)
        } else if chunk.Transcribe.Text != "" {
            // Final transcription
            fmt.Printf("\nFinal transcription: %s\n", chunk.Transcribe.Text)
        }
    }
}

fmt.Printf("\nComplete transcription: %s\n", fullTranscription.String())
```

### **🎵 Audio Round-Trip Example**

Complete workflow: text → speech → transcription:

```go
func audioRoundTrip(client *bifrost.Bifrost) error {
    ctx := context.Background()
    originalText := "The quick brown fox jumps over the lazy dog."
    
    // Step 1: Convert text to speech
    fmt.Println("Step 1: Converting text to speech...")
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
                ResponseFormat: "wav", // WAV for better transcription
            },
        },
    })
    if err != nil {
        return fmt.Errorf("speech synthesis failed: %v", err)
    }
    
    // Step 2: Save audio to temporary file (optional)
    tempFile := "temp_speech.wav"
    err = os.WriteFile(tempFile, speechResponse.Speech.Audio, 0644)
    if err != nil {
        return fmt.Errorf("failed to save audio: %v", err)
    }
    defer os.Remove(tempFile)
    
    // Step 3: Transcribe speech back to text
    fmt.Println("Step 2: Transcribing speech back to text...")
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
    
    // Step 4: Compare results
    fmt.Printf("Original text: %s\n", originalText)
    fmt.Printf("Transcribed text: %s\n", transcriptionResponse.Transcribe.Text)
    
    return nil
}
```

### **Cleanup**

Always cleanup resources when done:

```go
func (b *Bifrost) Cleanup()
```

**Example:**

```go
client, initErr := bifrost.Init(config)
if initErr != nil {
    log.Fatal(initErr)
}
defer client.Cleanup() // Ensures proper resource cleanup
```

---

## ⚡ Advanced Request Patterns

### **Multi-Turn Conversations**

Build conversational applications with message history:

```go
conversation := []schemas.BifrostMessage{
    {
        Role: schemas.ModelChatMessageRoleSystem,
        Content: schemas.MessageContent{ContentStr: &systemPrompt},
    },
    {
        Role: schemas.ModelChatMessageRoleUser,
        Content: schemas.MessageContent{ContentStr: &userMessage1},
    },
    {
        Role: schemas.ModelChatMessageRoleAssistant,
        Content: schemas.MessageContent{ContentStr: &assistantResponse1},
    },
    {
        Role: schemas.ModelChatMessageRoleUser,
        Content: schemas.MessageContent{ContentStr: &userMessage2},
    },
}

response, err := client.ChatCompletionRequest(ctx, &schemas.BifrostRequest{
    Provider: schemas.Anthropic,
    Model:    "claude-3-sonnet-20240229",
    Input: schemas.RequestInput{
        ChatCompletionInput: &conversation,
    },
})
```

### **Automatic Fallbacks**

Ensure reliability with provider fallbacks:

```go
response, err := client.ChatCompletionRequest(ctx, &schemas.BifrostRequest{
    Provider: schemas.OpenAI,        // Primary provider
    Model:    "gpt-4o-mini",
    Input:    input, // your input here
    Fallbacks: []schemas.Fallback{
        {Provider: schemas.Anthropic, Model: "claude-3-sonnet-20240229"},
        {Provider: schemas.Vertex, Model: "gemini-pro"},
        {Provider: schemas.Cohere, Model: "command-a-03-2025"},
    },
})

// Bifrost automatically tries fallbacks if primary fails
// Check which provider was actually used:
fmt.Printf("Used provider: %s\n", response.ExtraFields.Provider)
```

### **Request Parameters**

Fine-tune model behavior with parameters:

```go
temperature := 0.7
maxTokens := 1000
stopSequences := []string{"\n\n", "END"}

response, err := client.ChatCompletionRequest(ctx, &schemas.BifrostRequest{
    Provider: schemas.OpenAI,
    Model:    "gpt-4o-mini",
    Input:    input, // your input here
    Params: &schemas.ModelParameters{
        Temperature:     &temperature,
        MaxTokens:       &maxTokens,
        StopSequences:   &stopSequences,
    },
})
```

---

## 🛠️ Tool Calling

### **Basic Tool Usage**

Enable models to call external functions:

```go
// Define your tool
weatherTool := schemas.Tool{
    Type: "function",
    Function: schemas.Function{
        Name:        "get_weather",
        Description: "Get current weather for a location",
        Parameters: schemas.FunctionParameters{
            Type: "object",
            Properties: map[string]interface{}{
                "location": map[string]interface{}{
                    "type":        "string",
                    "description": "City name",
                },
                "unit": map[string]interface{}{
                    "type": "string",
                    "enum": []string{"celsius", "fahrenheit"},
                },
            },
            Required: []string{"location"},
        },
    },
}

// Make request with tools
auto := "auto"
response, err := client.ChatCompletionRequest(ctx, &schemas.BifrostRequest{
    Provider: schemas.OpenAI,
    Model:    "gpt-4o-mini",
    Input:    input, // your input here
    Params: &schemas.ModelParameters{
        Tools:      &[]schemas.Tool{weatherTool},
        ToolChoice: &schemas.ToolChoice{ToolChoiceStr: &auto},
    },
})

// Check if model wants to call tools
if len(response.Choices) > 0 && response.Choices[0].Message.ToolCalls != nil {
    for _, toolCall := range *response.Choices[0].Message.ToolCalls {
        if toolCall.Function.Name != nil && *toolCall.Function.Name == "get_weather" {
            // Handle the tool call
            result := handleWeatherCall(toolCall.Function.Arguments)

            // Add tool result to conversation and continue
            // ... (see MCP documentation for automated tool handling)
        }
    }
}
```

### **Tool Choice Control**

Control when and which tools the model uses:

```go
// Auto: Model decides whether to call tools
auto := "auto"
toolChoice := &schemas.ToolChoice{ToolChoiceStr: &auto}

// None: Never call tools
none := "none"
toolChoice := &schemas.ToolChoice{ToolChoiceStr: &none}

// Required: Must call at least one tool
required := "required"
toolChoice := &schemas.ToolChoice{ToolChoiceStr: &required}

// Specific function: Must call this specific tool
toolChoice := &schemas.ToolChoice{
    ToolChoiceStruct: &schemas.ToolChoiceStruct{
        Type: schemas.ToolChoiceTypeFunction,
        Function: schemas.ToolChoiceFunction{
            Name: "get_weather",
        },
    },
}
```

---

## 🖼️ Multimodal Requests

### **Image Analysis**

Send images for analysis (supported by GPT-4V, Claude, etc.):

```go
// Image from URL
imageMessage := schemas.BifrostMessage{
    Role: schemas.ModelChatMessageRoleUser,
    Content: schemas.MessageContent{
        ContentBlocks: &[]schemas.ContentBlock{
            {
                Type: schemas.ContentBlockTypeText,
                Text: bifrost.Ptr("What is this image about?"),
            },
            {
                Type: schemas.ContentBlockTypeImage,
                ImageURL: &schemas.ImageURLStruct{
                    URL:    "https://example.com/image.jpg",
                    Detail: &detail, // "high", "low", or "auto"
                },
            },
        },
    },
}

// Image from base64
base64Image := "data:image/jpeg;base64,/9j/4AAQSkZJRgABA..."
imageMessageBase64 := schemas.BifrostMessage{
    Role: schemas.ModelChatMessageRoleUser,
    Content: schemas.MessageContent{
        ContentBlocks: &[]schemas.ContentBlock{
            {
                Type: schemas.ContentBlockTypeText,
                Text: bifrost.Ptr("What is this image about?"),
            },
            {
                Type: schemas.ContentBlockTypeImage,
                ImageURL: &schemas.ImageURLStruct{
                    URL: base64Image,
                },
            },
        },
    },
}

response, err := client.ChatCompletionRequest(ctx, &schemas.BifrostRequest{
    Provider: schemas.OpenAI,
    Model:    "gpt-4o", // Multimodal model
    Input: schemas.RequestInput{
        ChatCompletionInput: &[]schemas.BifrostMessage{imageMessage},
    },
})
```

---

## 🔄 Context Management

### **Context with Timeouts**

Control request timeouts and cancellation:

```go
// Request with timeout
ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()

response, err := client.ChatCompletionRequest(ctx, request)
if err != nil {
    if ctx.Err() == context.DeadlineExceeded {
        fmt.Println("Request timed out")
    }
}

// Cancellable request
ctx, cancel := context.WithCancel(context.Background())

// Cancel from another goroutine
go func() {
    time.Sleep(5 * time.Second)
    cancel()
}()

response, err := client.ChatCompletionRequest(ctx, request)
```

### **Context with Values**

Pass metadata through request context:

```go
// Add request metadata
ctx := context.WithValue(context.Background(), "user_id", "user123")
ctx = context.WithValue(ctx, "session_id", "session456")

// Plugins can access these values
response, err := client.ChatCompletionRequest(ctx, request)
```

---

## 📊 Response Handling

### **Response Structure**

Understanding the response format:

```go
type BifrostResponse struct {
    ID                string                     `json:"id"`
    Object            string                     `json:"object"`
    Choices           []BifrostResponseChoice    `json:"choices"`
    Model             string                     `json:"model"`
    Created           int                        `json:"created"`
    Usage             LLMUsage                   `json:"usage"`
    ExtraFields       BifrostResponseExtraFields `json:"extra_fields"`
}

// Access response data
if len(response.Choices) > 0 {
    choice := response.Choices[0]

    // Text content
    if choice.Message.Content.ContentStr != nil {
        content := *choice.Message.Content.ContentStr
    }

    // Tool calls
    if choice.Message.ToolCalls != nil {
        for _, toolCall := range *choice.Message.ToolCalls {
            // Handle tool call
        }
    }

    // Finish reason
    if choice.FinishReason != nil {
        reason := *choice.FinishReason // "stop", "length", "tool_calls", etc.
    }
}

// Provider metadata
providerUsed := response.ExtraFields.Provider
latency := response.ExtraFields.Latency
tokenUsage := response.Usage
```

### **Error Handling**

Handle different types of errors:

```go
response, err := client.ChatCompletionRequest(ctx, request)
if err != nil {
    // Check if it's a Bifrost error
    if err.IsBifrostError {
        fmt.Printf("Bifrost error: %s\n", err.Error.Message)
    }

    // Check for specific error types
    if err.Error.Type != nil {
        switch *err.Error.Type {
        case schemas.RequestCancelled:
            fmt.Println("Request was cancelled")
        case schemas.ErrProviderRequest:
            fmt.Println("Provider request failed")
        default:
            fmt.Printf("Error type: %s\n", *err.Error.Type)
        }
    }

    // Check HTTP status code
    if err.StatusCode != nil {
        fmt.Printf("HTTP Status: %d\n", *err.StatusCode)
    }

    return
}
```

---

## 🔧 Advanced Configuration

### **Custom Initialization**

Configure client behavior during initialization:

```go
// Production configuration
client, initErr := bifrost.Init(schemas.BifrostConfig{
    Account:            &MyAccount{},
    Plugins:            []schemas.Plugin{&MyPlugin{}},
    Logger:             customLogger,
    InitialPoolSize:    200,           // Higher pool for performance
    DropExcessRequests: false,         // Wait for queue space (safer)
    MCPConfig: &schemas.MCPConfig{
        ClientConfigs: []schemas.MCPClientConfig{
            {
                Name:           "weather-tools",
                ConnectionType: schemas.MCPConnectionTypeSTDIO,
                StdioConfig: &schemas.MCPStdioConfig{
                    Command: "npx",
                    Args:    []string{"-y", "weather-mcp-server"},
                },
            },
        },
    },
})
```

### **Graceful Cleanup**

Always cleanup resources properly:

```go
func main() {
    client, initErr := bifrost.Init(config)
    if initErr != nil {
        log.Fatal(initErr)
    }

    // Setup graceful shutdown
    defer client.Cleanup()

    // Handle OS signals for clean shutdown
    c := make(chan os.Signal, 1)
    signal.Notify(c, os.Interrupt, syscall.SIGTERM)

    go func() {
        <-c
        fmt.Println("Shutting down gracefully...")
        client.Cleanup()
        os.Exit(0)
    }()

    // Your application logic
    // ...
}
```

---

## 🧪 Testing Client Usage

### **Unit Tests**

Test client methods with mock providers:

```go
func TestChatCompletion(t *testing.T) {
    account := &TestAccount{}
    client, initErr := bifrost.Init(schemas.BifrostConfig{
        Account: account,
    })
    require.Nil(t, initErr)
    defer client.Cleanup()

    message := "Hello, test!"
    response, err := client.ChatCompletionRequest(context.Background(), &schemas.BifrostRequest{
        Provider: schemas.OpenAI,
        Model:    "gpt-4o-mini",
        Input: schemas.RequestInput{
            ChatCompletionInput: &[]schemas.BifrostMessage{
                {Role: schemas.ModelChatMessageRoleUser, Content: schemas.MessageContent{ContentStr: &message}},
            },
        },
    })

    assert.NoError(t, err)
    assert.NotNil(t, response)
    assert.Greater(t, len(response.Choices), 0)
}
```

### **Integration Tests**

Test with real providers (requires API keys):

```go
func TestIntegrationChatCompletion(t *testing.T) {
    if testing.Short() {
        t.Skip("Skipping integration test")
    }

    // Requires real API key
    if os.Getenv("OPENAI_API_KEY") == "" {
        t.Skip("OPENAI_API_KEY not set")
    }

    account := &ProductionAccount{}
    client, initErr := bifrost.Init(schemas.BifrostConfig{
        Account: account,
    })
    require.Nil(t, initErr)
    defer client.Cleanup()

    // Test actual request
    message := "What is 2+2?"
    response, err := client.ChatCompletionRequest(context.Background(), &schemas.BifrostRequest{
        Provider: schemas.OpenAI,
        Model:    "gpt-4o-mini",
        Input: schemas.RequestInput{
            ChatCompletionInput: &[]schemas.BifrostMessage{
                {Role: schemas.ModelChatMessageRoleUser, Content: schemas.MessageContent{ContentStr: &message}},
            },
        },
    })

    assert.NoError(t, err)
    assert.Contains(t, *response.Choices[0].Message.Content.ContentStr, "4")
}
```

---

## 📚 Related Documentation

- **[🏛️ Account Interface](./account.md)** - Configure providers and keys
- **[🔌 Plugins](./plugins.md)** - Add custom middleware
- **[🛠️ MCP Integration](./mcp.md)** - Tool calling and external integrations
- **[📋 Schemas](./schemas.md)** - Data structures and interfaces reference
- **[🌐 HTTP Transport](../http-transport/)** - REST API alternative

> **🏛️ Architecture:** For system internals and performance details, see [Architecture Documentation](../../architecture/).
