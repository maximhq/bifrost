/**
 * Memory management utilities for WASM plugins.
 * Handles allocation, deallocation, and string read/write operations.
 */

// Pack a pointer and length into a single u64
// Upper 32 bits: pointer, Lower 32 bits: length
export function packResult(ptr: u32, len: u32): u64 {
  return (u64(ptr) << 32) | u64(len)
}

// Write a string to memory and return packed pointer+length
export function writeString(s: string): u64 {
  if (s.length === 0) {
    return 0
  }
  const encoded = String.UTF8.encode(s)
  const ptr = changetype<u32>(encoded)
  return packResult(ptr, encoded.byteLength)
}

// Read a string from memory given pointer and length
export function readString(ptr: u32, len: u32): string {
  if (len === 0) {
    return ''
  }
  const buffer = new ArrayBuffer(len)
  memory.copy(changetype<usize>(buffer), ptr, len)
  return String.UTF8.decode(buffer)
}

// Allocate memory for the host to write data
export function malloc(size: u32): u32 {
  if (size === 0) {
    return 0
  }
  const buffer = new ArrayBuffer(size)
  return changetype<u32>(buffer)
}

// Free allocated memory (handled by AssemblyScript runtime)
export function free(_ptr: u32): void {
  // AssemblyScript handles garbage collection
  // This is provided for API compatibility
}
