import React from 'react';
import { Card } from '@/components/ui/card';
import { Skeleton } from '@/components/ui/skeleton';

interface ImageMessageProps {
  images: Array<{
    url?: string;
    b64_json?: string;
    revised_prompt?: string;
    index: number;
  }>;
  isStreaming?: boolean;
  streamProgress?: number; // 0-100
}

export const ImageMessage: React.FC<ImageMessageProps> = ({
  images,
  isStreaming,
  streamProgress
}) => {
  return (
    <div className="grid grid-cols-2 gap-4 my-4">
      {images.map((img, idx) => (
        <Card key={idx} className="overflow-hidden">
          {isStreaming && !img.url && !img.b64_json ? (
  <div className="relative">
    <Skeleton className="w-full aspect-square" />
    <div className="absolute bottom-2 left-2 text-sm text-muted-foreground">
      Loading... {streamProgress}%
    </div>
  </div>
) : (img.url || img.b64_json) ? (
  <>
    <img
      src={img.url || `data:image/png;base64,${img.b64_json}`}
      alt={img.revised_prompt || `Generated image ${idx + 1}`}
      className="w-full h-auto"
      loading="lazy"
    />
    {img.revised_prompt && (
      <div className="p-2 text-xs text-muted-foreground border-t">
        {img.revised_prompt}
      </div>
    )}
  </>
) : (
  <div className="flex items-center justify-center aspect-square p-4 bg-muted/50">
    <p className="text-sm text-muted-foreground">
      Image unavailable
    </p>
  </div>
)}
        </Card>
      ))}
    </div>
  );
};