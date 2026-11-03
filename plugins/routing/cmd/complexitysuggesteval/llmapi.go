package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
)

// apiClient talks to any OpenAI-compatible endpoint (OpenAI itself, or a
// local Bifrost with provider-prefixed model names). Embeddings and chat
// answers are cached on disk so reruns while tuning cost nothing.
type apiClient struct {
	baseURL    string
	key        string
	embedModel string
	chatModel  string
	http       *http.Client

	mu        sync.Mutex
	cachePath string
	cache     diskCache
	dirty     bool
}

type diskCache struct {
	Embeddings map[string][]float32
	Chats      map[string]string
}

func newAPIClient(baseURL, key, embedModel, chatModel, cachePath string) *apiClient {
	c := &apiClient{
		baseURL: strings.TrimRight(baseURL, "/"), key: key,
		embedModel: embedModel, chatModel: chatModel,
		http:      &http.Client{Timeout: 3 * time.Minute},
		cachePath: cachePath,
		cache:     diskCache{Embeddings: map[string][]float32{}, Chats: map[string]string{}},
	}
	if f, err := os.Open(cachePath); err == nil {
		defer f.Close()
		var loaded diskCache
		if gob.NewDecoder(f).Decode(&loaded) == nil {
			if loaded.Embeddings != nil {
				c.cache.Embeddings = loaded.Embeddings
			}
			if loaded.Chats != nil {
				c.cache.Chats = loaded.Chats
			}
		}
	}
	return c
}

func (c *apiClient) save() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.dirty {
		return nil
	}
	tmp := c.cachePath + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := gob.NewEncoder(f).Encode(c.cache); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	c.dirty = false
	return os.Rename(tmp, c.cachePath)
}

func cacheKey(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		fmt.Fprintf(h, "%d:%s|", len(p), p)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (c *apiClient) post(ctx context.Context, path string, body any, out any) error {
	payload, err := sonic.Marshal(body)
	if err != nil {
		return err
	}
	var lastErr error
	for attempt := range 5 {
		if attempt > 0 {
			time.Sleep(time.Duration(1<<attempt) * time.Second)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		if c.key != "" {
			req.Header.Set("Authorization", "Bearer "+c.key)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("%s: HTTP %d: %s", path, resp.StatusCode, truncate(string(data), 300))
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("%s: HTTP %d: %s", path, resp.StatusCode, truncate(string(data), 500))
		}
		return sonic.Unmarshal(data, out)
	}
	return lastErr
}

// embed returns unit-length vectors for texts, in order.
func (c *apiClient) embed(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	var missing []int
	c.mu.Lock()
	for i, t := range texts {
		if v, ok := c.cache.Embeddings[cacheKey(c.embedModel, t)]; ok {
			out[i] = v
		} else {
			missing = append(missing, i)
		}
	}
	c.mu.Unlock()

	const batch = 128
	for start := 0; start < len(missing); start += batch {
		end := min(start+batch, len(missing))
		inputs := make([]string, 0, end-start)
		for _, idx := range missing[start:end] {
			inputs = append(inputs, texts[idx])
		}
		var resp struct {
			Data []struct {
				Index     int       `json:"index"`
				Embedding []float32 `json:"embedding"`
			} `json:"data"`
		}
		if err := c.post(ctx, "/embeddings", map[string]any{"model": c.embedModel, "input": inputs}, &resp); err != nil {
			return nil, err
		}
		if len(resp.Data) != len(inputs) {
			return nil, fmt.Errorf("embeddings: asked for %d, got %d", len(inputs), len(resp.Data))
		}
		c.mu.Lock()
		for _, d := range resp.Data {
			idx := missing[start+d.Index]
			v := normalize(d.Embedding)
			out[idx] = v
			c.cache.Embeddings[cacheKey(c.embedModel, texts[idx])] = v
		}
		c.dirty = true
		c.mu.Unlock()
		fmt.Fprintf(os.Stderr, "  embedded %d/%d new texts\n", end, len(missing))
	}
	return out, nil
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatJSON runs one chat completion in JSON mode and returns the content.
func (c *apiClient) chatJSON(ctx context.Context, messages []chatMessage) (string, error) {
	return c.chatJSONModel(ctx, c.chatModel, messages)
}

// chatJSONModel is chatJSON with an explicit model, so the held-out judge can
// differ from the model that writes suggestions. Cache keys are unchanged.
func (c *apiClient) chatJSONModel(ctx context.Context, model string, messages []chatMessage) (string, error) {
	keyParts := []string{model}
	for _, m := range messages {
		keyParts = append(keyParts, m.Role, m.Content)
	}
	key := cacheKey(keyParts...)
	c.mu.Lock()
	if v, ok := c.cache.Chats[key]; ok {
		c.mu.Unlock()
		return v, nil
	}
	c.mu.Unlock()

	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	body := map[string]any{
		"model":           model,
		"messages":        messages,
		"response_format": map[string]string{"type": "json_object"},
	}
	if err := c.post(ctx, "/chat/completions", body, &resp); err != nil {
		return "", err
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("chat: no choices")
	}
	content := resp.Choices[0].Message.Content
	c.mu.Lock()
	c.cache.Chats[key] = content
	c.dirty = true
	c.mu.Unlock()
	return content, nil
}

func normalize(v []float32) []float32 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	n := float32(math.Sqrt(sum))
	if n == 0 {
		return v
	}
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = x / n
	}
	return out
}

func dot(a, b []float32) float32 {
	var s float32
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// parallel runs fn over [0,n) with the given number of workers and returns the
// first error.
func parallel(n, workers int, fn func(i int) error) error {
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		first   error
		indices = make(chan int)
	)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range indices {
				if err := fn(i); err != nil {
					mu.Lock()
					if first == nil {
						first = err
					}
					mu.Unlock()
				}
			}
		}()
	}
	for i := range n {
		indices <- i
	}
	close(indices)
	wg.Wait()
	return first
}
