//! Memory management utilities for WASM plugins.
//! Handles allocation, deallocation, and string read/write operations.

use std::alloc::{alloc, dealloc, Layout};
use std::slice;

/// Pack a pointer and length into a single u64
/// Upper 32 bits: pointer, Lower 32 bits: length
pub fn pack_result(ptr: u32, len: u32) -> u64 {
    ((ptr as u64) << 32) | (len as u64)
}

/// Write a string to WASM memory and return packed pointer+length
pub fn write_string(s: &str) -> u64 {
    if s.is_empty() {
        return 0;
    }
    let bytes = s.as_bytes();
    let ptr = unsafe { malloc(bytes.len() as u32) };
    if ptr == 0 {
        return 0;
    }
    unsafe {
        std::ptr::copy_nonoverlapping(bytes.as_ptr(), ptr as *mut u8, bytes.len());
    }
    pack_result(ptr, bytes.len() as u32)
}

/// Read a string from WASM memory given pointer and length
pub fn read_string(ptr: u32, len: u32) -> String {
    if len == 0 {
        return String::new();
    }
    let bytes = unsafe { slice::from_raw_parts(ptr as *const u8, len as usize) };
    String::from_utf8_lossy(bytes).into_owned()
}

/// Allocate memory for the host to write data
/// 
/// # Safety
/// This function is marked as safe but performs unsafe operations internally.
/// It is intended to be called from WASM host.
#[no_mangle]
pub extern "C" fn malloc(size: u32) -> u32 {
    if size == 0 {
        return 0;
    }
    let layout = match Layout::from_size_align(size as usize, 1) {
        Ok(l) => l,
        Err(_) => return 0,
    };
    unsafe { alloc(layout) as u32 }
}

/// Free allocated memory
/// 
/// # Safety
/// This function is marked as safe but performs unsafe operations internally.
/// It is intended to be called from WASM host.
#[no_mangle]
pub extern "C" fn free(ptr: u32, size: u32) {
    if ptr == 0 || size == 0 {
        return;
    }
    let layout = match Layout::from_size_align(size as usize, 1) {
        Ok(l) => l,
        Err(_) => return,
    };
    unsafe { dealloc(ptr as *mut u8, layout) }
}
