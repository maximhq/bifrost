package main

import "unsafe"

// ============================================================================
// Memory Management
// ============================================================================

// heapSize is the fixed size of the pre-allocated heap.
// This must be large enough to handle all allocations during the plugin lifetime.
// The heap is never reallocated to ensure all pointers remain valid.
const heapSize = 4 * 1024 * 1024 // 4MB fixed heap

// heapBase is a fixed-size buffer that is never reallocated.
// All allocations come from this buffer to ensure pointer stability.
var heapBase []byte

// heapOffset tracks the next available position in heapBase.
var heapOffset uint32 = 0

// heapBasePtr caches the base pointer of heapBase for efficient offset-to-pointer conversion.
var heapBasePtr uintptr

func init() {
	// Pre-allocate the fixed heap once at startup.
	// This ensures heapBase is never reallocated after pointers are handed out.
	heapBase = make([]byte, heapSize)
	heapBasePtr = uintptr(unsafe.Pointer(&heapBase[0]))
}

//export plugin_malloc
func plugin_malloc(size uint32) uint32 {
	if size == 0 {
		return 0
	}
	// Align to 8-byte boundary
	alignedSize := (size + 7) &^ 7
	// Check if we have enough space (no reallocation allowed)
	if heapOffset+alignedSize > uint32(len(heapBase)) {
		// Allocation failure - heap exhausted
		// Return 0 to indicate failure rather than reallocating
		return 0
	}
	// Return pointer to the allocated region
	ptr := uint32(heapBasePtr + uintptr(heapOffset))
	heapOffset += alignedSize
	return ptr
}

//export plugin_free
func plugin_free(ptr uint32) {
	// No-op: we use a simple bump allocator without individual frees.
	// Memory is reclaimed when the plugin is unloaded.
}

// plugin_reset resets the heap allocator, allowing memory to be reused.
// This should only be called when no allocated memory is in use.
//
//export plugin_reset
func plugin_reset() {
	heapOffset = 0
}

func packResult(ptr uint32, length uint32) uint64 {
	return (uint64(ptr) << 32) | uint64(length)
}

func writeBytes(data []byte) uint64 {
	if len(data) == 0 {
		return 0
	}
	// Allocate from the stable heap
	ptr := plugin_malloc(uint32(len(data)))
	if ptr == 0 {
		// Allocation failed
		return 0
	}
	// Copy data into the allocated region
	offset := ptr - uint32(heapBasePtr)
	copy(heapBase[offset:offset+uint32(len(data))], data)
	return packResult(ptr, uint32(len(data)))
}

func readInput(ptr, length uint32) []byte {
	if length == 0 {
		return nil
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(uintptr(ptr))), length)
}
