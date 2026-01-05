import React from 'react';
import { Card } from '@/components/ui/card';
import { Skeleton } from '@/components/ui/skeleton';

interface ImageMessageProps {
  image: {
    url?: string;
    b64_json?: string;
    prompt?: string;
    revised_prompt?: string;
    index?: number;
  } | null;
  isStreaming?: boolean;
  streamProgress?: number; // 0-100
}

export const ImageMessage: React.FC<ImageMessageProps> = ({
  image,
  isStreaming,
  streamProgress
}) => {
  // Show loading state if streaming but no image yet
  if (isStreaming && !image) {
    return (
      <div className="my-4">
        <Card className="overflow-hidden">
          <div className="relative">
            <Skeleton className="w-full aspect-square" />
            <div className="absolute bottom-2 left-2 text-sm text-muted-foreground">
              Loading... {streamProgress}%
            </div>
          </div>
        </Card>
      </div>
    );
  }

  // Only show image if it has actual image data (url or b64_json)
  if (!image || (!image.url && !image.b64_json)) {
    return null;
  }

  return (
    <div className="my-4">
      <Card className="overflow-hidden">
        <div className="w-full rounded-lg border border-border p-[2px] overflow-hidden">
          <img
            src={image.url || `data:image/png;base64,${image.b64_json}`}
            alt={image.prompt || `image-${image.index ?? 0}`}
            className="w-full h-auto rounded-md"
            loading="lazy"
          />
        </div>
      </Card>
    </div>
  );
};