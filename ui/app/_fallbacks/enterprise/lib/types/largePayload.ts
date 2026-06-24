export interface LargePayloadConfig {
	enabled: boolean;
	request_threshold_bytes: number;
	response_threshold_bytes: number;
	prefetch_size_bytes: number;
	max_payload_bytes: number;
	truncated_log_bytes: number;
}

export const DefaultLargePayloadConfig: LargePayloadConfig = {
	enabled: false,
	request_threshold_bytes: 10 * 1024 * 1024, // 10MB
	response_threshold_bytes: 10 * 1024 * 1024, // 10MB
	prefetch_size_bytes: 64 * 1024, // 64KB
	max_payload_bytes: 500 * 1024 * 1024, // 500MB
	truncated_log_bytes: 1024 * 1024, // 1MB
};