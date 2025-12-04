"use client";

import { useState, useCallback, useRef, useEffect } from "react";
import { getEndpointUrl } from "@/lib/utils/port";
import { getTokenFromStorage } from "@/lib/store/apis/baseApi";

// Matches backend BifrostImageGenerationStreamResponse
interface ImageStreamChunk {
  id: string;
  type: string; // "image_generation.partial_image", "image_generation.completed", "error"
  index: number; // Which image (0-N)
  chunk_index: number; // Chunk order within image
  partial_b64?: string; // Base64 chunk
  revised_prompt?: string; // On first chunk
  usage?: {
    prompt_tokens: number;
    total_tokens: number;
  };
  error?: {
    message: string;
    code?: string;
  };
}

export interface StreamedImage {
  url?: string;
  b64_json?: string;
  revised_prompt?: string;
  index: number;
}

interface ImageStreamState {
  images: StreamedImage[];
  isStreaming: boolean;
  progress: number; // 0-100
  error: string | null;
}

interface UseImageStreamOptions {
  onComplete?: (images: StreamedImage[]) => void;
  onError?: (error: string) => void;
}

interface ImageStreamRequest {
  model: string;
  prompt: string;
  n?: number;
  size?: string;
  quality?: string;
  style?: string;
  response_format?: string;
}

export function useImageStream(options: UseImageStreamOptions = {}) {
  const [state, setState] = useState<ImageStreamState>({
    images: [],
    isStreaming: false,
    progress: 0,
    error: null,
  });

  const abortControllerRef = useRef<AbortController | null>(null);
  const imageChunksRef = useRef<Map<number, { chunks: Map<number, string>; revisedPrompt?: string }>>(new Map());
  const totalChunksReceivedRef = useRef(0);
  const expectedImagesRef = useRef(1);

  // Reset state for new request
  const reset = useCallback(() => {
    imageChunksRef.current.clear();
    totalChunksReceivedRef.current = 0;
    setState({
      images: [],
      isStreaming: false,
      progress: 0,
      error: null,
    });
  }, []);

  // Cancel ongoing stream
  const cancel = useCallback(() => {
    if (abortControllerRef.current) {
      abortControllerRef.current.abort();
      abortControllerRef.current = null;
    }
    setState((prev) => ({ ...prev, isStreaming: false }));
  }, []);

  // Build complete image from accumulated chunks
  const buildImageFromChunks = useCallback((imageIndex: number): StreamedImage | null => {
    const imageData = imageChunksRef.current.get(imageIndex);
    if (!imageData) return null;

    // Sort chunks by chunk_index and concatenate
    const sortedChunks = Array.from(imageData.chunks.entries())
      .sort(([a], [b]) => a - b)
      .map(([, chunk]) => chunk);

    const fullB64 = sortedChunks.join("");

    return {
      b64_json: fullB64,
      revised_prompt: imageData.revisedPrompt,
      index: imageIndex,
    };
  }, []);

  // Process incoming chunk
  const processChunk = useCallback(
    (chunk: ImageStreamChunk) => {
      const { index, chunk_index, partial_b64, revised_prompt, type, error } = chunk;

      // Handle errors
      if (type === "error" || error) {
        const errorMsg = error?.message || "Unknown streaming error";
        setState((prev) => ({ ...prev, error: errorMsg, isStreaming: false }));
        options.onError?.(errorMsg);
        return;
      }

      // Initialize image data if needed
      if (!imageChunksRef.current.has(index)) {
        imageChunksRef.current.set(index, { chunks: new Map() });
      }

      const imageData = imageChunksRef.current.get(index)!;

      // Store revised prompt (usually on first chunk)
      if (revised_prompt) {
        imageData.revisedPrompt = revised_prompt;
      }

      // Store chunk data
      if (partial_b64) {
        imageData.chunks.set(chunk_index, partial_b64);
        totalChunksReceivedRef.current++;
      }

      // Calculate progress (rough estimate based on chunks received)
      // Assuming ~10 chunks per image on average
      const estimatedTotalChunks = expectedImagesRef.current * 10;
      const progress = Math.min(95, Math.round((totalChunksReceivedRef.current / estimatedTotalChunks) * 100));

      // Handle completion
      if (type === "image_generation.completed") {
        const completedImage = buildImageFromChunks(index);

        setState((prev) => {
          const newImages = [...prev.images];
          if (completedImage) {
            // Replace or add the completed image
            const existingIdx = newImages.findIndex((img) => img.index === index);
            if (existingIdx >= 0) {
              newImages[existingIdx] = completedImage;
            } else {
              newImages.push(completedImage);
            }
          }

          // Check if all images are complete
          const allComplete = newImages.length >= expectedImagesRef.current;

          if (allComplete) {
            options.onComplete?.(newImages);
          }

          return {
            ...prev,
            images: newImages.sort((a, b) => a.index - b.index),
            isStreaming: !allComplete,
            progress: allComplete ? 100 : progress,
          };
        });
      } else {
        // Update progress during streaming
        setState((prev) => ({ ...prev, progress }));
      }
    },
    [buildImageFromChunks, options]
  );

  // Start streaming request
  const stream = useCallback(
    async (request: ImageStreamRequest) => {
      reset();
      expectedImagesRef.current = request.n || 1;

      setState((prev) => ({ ...prev, isStreaming: true, error: null }));

      abortControllerRef.current = new AbortController();

      try {
        const token = await getTokenFromStorage();
        const url = getEndpointUrl("/v1/images/generations");

        const response = await fetch(url, {
          method: "POST",
          headers: {
            "Content-Type": "application/json",
            ...(token ? { Authorization: `Bearer ${token}` } : {}),
          },
          body: JSON.stringify({
            ...request,
            stream: true,
          }),
          signal: abortControllerRef.current.signal,
        });

        if (!response.ok) {
          const errorText = await response.text();
          throw new Error(errorText || `HTTP ${response.status}`);
        }

        if (!response.body) {
          throw new Error("No response body");
        }

        const reader = response.body.getReader();
        const decoder = new TextDecoder();
        let buffer = "";

        while (true) {
          const { done, value } = await reader.read();

          if (done) {
            break;
          }

          buffer += decoder.decode(value, { stream: true });

          // Process SSE lines
          const lines = buffer.split("\n");
          buffer = lines.pop() || ""; // Keep incomplete line in buffer

          for (const line of lines) {
            const trimmed = line.trim();

            if (trimmed.startsWith("data: ")) {
              const data = trimmed.slice(6);

              if (data === "[DONE]") {
                setState((prev) => ({ ...prev, isStreaming: false, progress: 100 }));
                return;
              }

              try {
                const chunk: ImageStreamChunk = JSON.parse(data);
                processChunk(chunk);
              } catch (parseError) {
                console.error("Failed to parse SSE chunk:", parseError);
              }
            }
          }
        }

        // Stream ended
        setState((prev) => ({ ...prev, isStreaming: false }));
      } catch (err) {
        if (err instanceof Error && err.name === "AbortError") {
          // Cancelled by user
          return;
        }

        const errorMsg = err instanceof Error ? err.message : "Stream failed";
        setState((prev) => ({ ...prev, error: errorMsg, isStreaming: false }));
        options.onError?.(errorMsg);
      }
    },
    [reset, processChunk, options]
  );

  // Cleanup on unmount
  useEffect(() => {
    return () => {
      if (abortControllerRef.current) {
        abortControllerRef.current.abort();
      }
    };
  }, []);

  return {
    ...state,
    stream,
    cancel,
    reset,
  };
}