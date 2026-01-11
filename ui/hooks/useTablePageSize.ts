"use client"

import { RefObject, useCallback, useEffect, useState } from "react"

const ROW_HEIGHT = 48 // h-12 = 3rem = 48px
const HEADER_HEIGHT = 44 // approximate table header height
const STATUS_ROW_HEIGHT = 48 // the "Listening for logs..." row (h-12)
const MIN_PAGE_SIZE = 5 // minimum items per page

interface UseTablePageSizeOptions {
  debounceMs?: number
}

export function useTablePageSize (
  containerRef: RefObject<HTMLElement | null>,
  options: UseTablePageSizeOptions = {}
): number | null {
  const { debounceMs = 150 } = options
  const [pageSize, setPageSize] = useState<number | null>(null)

  const calculatePageSize = useCallback((height: number): number => {
    const availableHeight = height - HEADER_HEIGHT - STATUS_ROW_HEIGHT
    const calculated = Math.floor(availableHeight / ROW_HEIGHT)
    return Math.max(calculated, MIN_PAGE_SIZE)
  }, [])

  useEffect(() => {
    const element = containerRef.current
    if (!element) return

    let timeoutId: ReturnType<typeof setTimeout> | null = null

    const handleResize = (entries: ResizeObserverEntry[]) => {
      const entry = entries[0]
      if (!entry) return

      const height = entry.contentRect.height

      if (timeoutId) {
        clearTimeout(timeoutId)
      }

      timeoutId = setTimeout(() => {
        const newPageSize = calculatePageSize(height)
        setPageSize(newPageSize)
      }, debounceMs)
    }

    const resizeObserver = new ResizeObserver(handleResize)
    resizeObserver.observe(element)

    // Calculate initial size immediately
    const initialHeight = element.getBoundingClientRect().height
    if (initialHeight > 0) {
      setPageSize(calculatePageSize(initialHeight))
    }

    return () => {
      if (timeoutId) {
        clearTimeout(timeoutId)
      }
      resizeObserver.disconnect()
    }
  }, [containerRef, calculatePageSize, debounceMs])

  return pageSize
}
