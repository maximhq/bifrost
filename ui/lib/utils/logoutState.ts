// Process-wide "a logout is in progress or has completed" latch.
//
// Once sign-out starts, every request still in flight (polled dashboard
// queries, the websocket ticket fetch) comes back 401. Without this latch each
// of those 401s would independently try a token refresh and then call logout
// again, producing dozens of logout requests from a single click. The latch is
// module state, so a full page load clears it; a client-side login clears it
// explicitly via endLogout().

let loggingOut = false;

export const beginLogout = (): void => {
	loggingOut = true;
};

export const endLogout = (): void => {
	loggingOut = false;
};

export const isLoggingOut = (): boolean => loggingOut;