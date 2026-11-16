// OSS build has no Edge device backend - return undefined so consumers fall
// back to rendering the raw device id.
export const useGetDeviceQuery = (
	_id: string,
	_opts?: { skip?: boolean },
): {
	data: { device: { id: string; hostname: string; platform: string; os_version: string; arch: string } } | undefined;
	isLoading: boolean;
	isError: boolean;
	error: null;
} => ({
	data: undefined,
	isLoading: false,
	isError: false,
	error: null,
});