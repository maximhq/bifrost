// Module-level lifecycle for the dashboard websocket. The socket outlives any
// single WebSocketProvider mount so a shell re-render does not tear the
// connection down, which means nothing that keeps the socket healthy may live
// in a provider's closure or refs: a provider that unmounts must not take the
// heartbeat with it, and a provider that mounts onto an existing socket must be
// able to observe that socket's open/close itself rather than through the
// handlers a previous (possibly unmounted) provider installed.

// Mirror of WebSocket.CONNECTING / OPEN so this module does not depend on the
// browser global being present (it is not, under vitest's node environment).
export const WS_CONNECTING = 0;
export const WS_OPEN = 1;

export const HEARTBEAT_INTERVAL_MS = 25000;

let socket: WebSocket | null = null;
let heartbeatTimer: ReturnType<typeof setInterval> | null = null;
let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
let retryCount = 0;
// Set to false once auth is gone (logout, or the ticket endpoint answering
// 401/403). Nothing reconnects until the next provider mount re-enables it.
let reconnectEnabled = true;
// The connect function of the currently mounted provider, or null. A close
// event schedules a reconnect only through this, never through the closure
// that created the socket.
let activeConnect: (() => void) | null = null;

export function getSocket() {
	return socket;
}

export function setSocket(ws: WebSocket | null) {
	socket = ws;
}

export function isReconnectEnabled() {
	return reconnectEnabled;
}

export function setReconnectEnabled(enabled: boolean) {
	reconnectEnabled = enabled;
}

export function setActiveConnect(connect: (() => void) | null) {
	activeConnect = connect;
}

export function resetRetryCount() {
	retryCount = 0;
}

export function clearReconnectTimer() {
	if (reconnectTimer) {
		clearTimeout(reconnectTimer);
		reconnectTimer = null;
	}
}

export function scheduleReconnect() {
	if (!reconnectEnabled || !activeConnect || reconnectTimer) {
		return;
	}
	// Exponential backoff: 1s, 2s, 4s, 8s, 16s, 32s (max)
	retryCount = Math.min(retryCount + 1, 6);
	const delay = Math.pow(2, retryCount) * 500;
	reconnectTimer = setTimeout(() => {
		reconnectTimer = null;
		activeConnect?.();
	}, delay);
}

export function stopHeartbeat() {
	if (heartbeatTimer) {
		clearInterval(heartbeatTimer);
		heartbeatTimer = null;
	}
}

/** Starts (or restarts) the keep-alive ping on `ws`. Owned by the socket, not by a provider mount. */
export function startHeartbeat(ws: WebSocket) {
	stopHeartbeat();
	heartbeatTimer = setInterval(() => {
		if (ws.readyState === WS_OPEN) {
			try {
				ws.send("ping");
			} catch (error) {
				console.error("Error sending ping:", error);
			}
		}
	}, HEARTBEAT_INTERVAL_MS);
}

/**
 * Binds a freshly mounted provider to a socket that already exists. The
 * creating provider's onopen/onclose handlers may belong to an unmounted
 * instance, so the adopter listens for open/close itself, and re-arms the
 * heartbeat if the socket is open without one. Returns a detach function for
 * the adopter's cleanup.
 */
export function adoptSocket(ws: WebSocket, handlers: { onOpen: () => void; onClose: () => void }): () => void {
	const onOpen = () => handlers.onOpen();
	const onClose = () => handlers.onClose();
	ws.addEventListener("open", onOpen);
	ws.addEventListener("close", onClose);
	if (ws.readyState === WS_OPEN && !heartbeatTimer) {
		startHeartbeat(ws);
	}
	return () => {
		ws.removeEventListener("open", onOpen);
		ws.removeEventListener("close", onClose);
	};
}

/**
 * Tears down the shared websocket and disables reconnection. Called on
 * sign-out so the client stops requesting tickets for a session that no
 * longer exists. The next WebSocketProvider mount (i.e. the next login)
 * re-enables reconnection.
 */
export function stopWebSocket() {
	reconnectEnabled = false;
	clearReconnectTimer();
	stopHeartbeat();
	const ws = socket;
	socket = null;
	if (ws) {
		// Detach first so the close event does not try to reconnect.
		ws.onclose = null;
		ws.onerror = null;
		try {
			ws.close();
		} catch {
			// Already closed.
		}
	}
}
