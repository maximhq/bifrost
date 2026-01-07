import React from 'react';
import { Card } from '@/components/ui/card';
import { Skeleton } from '@/components/ui/skeleton';
import { ImageMessageData } from '@/lib/types/logs';

interface ImageMessageProps {
  image: ImageMessageData | null;
  isStreaming?: boolean;
  streamProgress?: number; // 0-100
}

export const ImageMessage: React.FC<ImageMessageProps> = ({
  image,
  isStreaming,
  streamProgress,
}) => {
  // Streaming placeholder - show when streaming and (no image or image has no usable data)
  const hasUsableImageData = image && (image.url || image.b64_json);
  if (isStreaming && (!image || !hasUsableImageData)) {
    return (
      <div className="my-4">
        <Card className="overflow-hidden">
          <div className="relative">
            <Skeleton className="w-full aspect-square" />
            <div className="absolute bottom-2 left-2 text-sm text-muted-foreground">
              Loading... {streamProgress ?? 0}%
            </div>
          </div>
        </Card>
      </div>
    );
  }

  // No usable image data
  if (!image || (!image.url && !image.b64_json)) {
    return null;
  }

  return (
    <div className="my-4">
      <Card className="p-0">
        <div className="border border-border overflow-auto">
          <img
            src={image.url || `data:image/png;base64,${image.b64_json}`}
            alt={image.prompt || `image-${image.index ?? 0}`}
            className="w-auto h-auto"
            loading="lazy"
          />
        </div>
      </Card>
    </div>
  );
};