import { getApiBaseUrl } from "@/lib/utils/port";
import { getWebSocketUrl } from "@/lib/utils/port";
import { isLoggingOut } from "@/lib/utils/logoutState";
import React, { createContext, useCallback, useContext, useEffect, useRef, useState, type ReactNode } from "react";
import {
	WS_CONNECTING,
	WS_OPEN,
	adoptSocket,
	clearReconnectTimer,
	getSocket,
	isReconnectEnabled,
	resetRetryCount,
	scheduleReconnect,
	setActiveConnect,
	setReconnectEnabled,
	setSocket,
	startHeartbeat,
	stopHeartbeat,
} from "./useWebSocket.utils";

export { stopWebSocket } from "./useWebSocket.utils";

type MessageHandler = (data: any) => void;

interface WebSocketContextType {
	isConnected: boolean;
	ws: React.RefObject<WebSocket | null>;
	subscribe: (channel: string, handler: MessageHandler) => () => void;
	send: (data: any) => void;
}

const WebSocketContext = createContext<WebSocketContextType | null>(null);

interface WebSocketProviderProps {
	children: ReactNode;
	path?: string;
}

// The socket, heartbeat and reconnect bookkeeping all live in
// useWebSocket.utils so they outlive any single provider mount: a shell
// re-render must not tear the connection down, and a close event that fires
// after the provider unmounted consults that shared state instead of a stale
// closure. Only subscriptions and per-mount React state live here.
const messageHandlers = new Map<string, Set<MessageHandler>>();

export function WebSocketProvider({ children, path = "/ws" }: WebSocketProviderProps) {
	const wsRef = useRef<WebSocket | null>(getSocket());
	const [isConnected, setIsConnected] = useState(getSocket()?.readyState === WS_OPEN);

	const subscribe = useCallback<(channel: string, handler: MessageHandler) => () => void>((channel, handler) => {
		if (!messageHandlers.has(channel)) {
			messageHandlers.set(channel, new Set());
		}
		messageHandlers.get(channel)!.add(handler);

		// Return unsubscribe function
		return () => {
			const handlers = messageHandlers.get(channel);
			if (handlers) {
				handlers.delete(handler);
				if (handlers.size === 0) {
					messageHandlers.delete(channel);
				}
			}
		};
	}, []);

	const send = (data: any) => {
		if (wsRef.current?.readyState === WS_OPEN) {
			try {
				wsRef.current.send(typeof data === "string" ? data : JSON.stringify(data));
			} catch (error) {
				console.error("Error sending message:", error);
			}
		}
	};

	useEffect(() => {
		let mounted = true;
		// Set when this mount adopts a socket created by an earlier mount;
		// removes the open/close listeners that keep this mount's state honest.
		let detachAdopted: (() => void) | null = null;

		const connect = async () => {
			if (!mounted || !isReconnectEnabled() || isLoggingOut()) {
				return;
			}
			const existing = getSocket();
			if (existing && (existing.readyState === WS_OPEN || existing.readyState === WS_CONNECTING)) {
				// Another mount created this socket; its onopen/onclose cannot
				// update this mount's state, so observe the socket directly.
				wsRef.current = existing;
				detachAdopted?.();
				detachAdopted = adoptSocket(existing, {
					onOpen: () => {
						if (mounted) setIsConnected(true);
					},
					onClose: () => {
						if (mounted) setIsConnected(false);
					},
				});
				if (existing.readyState === WS_OPEN) setIsConnected(true);
				return;
			}

			const wsUrl = getWebSocketUrl(path);
			// Obtain a short-lived, single-use ticket for WS auth instead of putting the session token in the URL.
			let wsUrlWithAuth = wsUrl;
			try {
				const resp = await fetch(`${getApiBaseUrl()}/session/ws-ticket`, {
					method: "POST",
					credentials: "include",
				});
				if (resp.status === 401 || resp.status === 403) {
					// The session is gone. Retrying cannot succeed until the user
					// signs in again, at which point the dashboard shell remounts
					// this provider and re-enables reconnection.
					setReconnectEnabled(false);
					if (mounted) setIsConnected(false);
					return;
				}
				if (resp.ok) {
					const { ticket } = await resp.json();
					if (ticket) {
						const parsed = new URL(wsUrl);
						parsed.searchParams.set("ticket", ticket);
						wsUrlWithAuth = parsed.toString();
					}
				}
			} catch {
				// If ticket fetch fails, attempt connection without auth param (cookie fallback)
			}
			// The ticket fetch is async; the provider may have unmounted or a
			// logout may have started while it was in flight.
			if (!mounted || !isReconnectEnabled() || isLoggingOut()) {
				return;
			}
			const ws = new WebSocket(wsUrlWithAuth);
			wsRef.current = ws;
			setSocket(ws);

			ws.onopen = () => {
				if (getSocket() !== ws) return;
				if (mounted) setIsConnected(true);
				resetRetryCount(); // Reset retry count on successful connection
				clearReconnectTimer();
				// The heartbeat belongs to the socket, not to this mount: a
				// later mount that adopts the socket must not find it silent.
				startHeartbeat(ws);
			};

			ws.onmessage = (event) => {
				try {
					const data = JSON.parse(event.data);
					const messageType = data.type || "default";

					// Notify all subscribers for this message type
					const handlers = messageHandlers.get(messageType);
					if (handlers) {
						handlers.forEach((handler) => handler(data));
					}

					// Also notify wildcard subscribers
					const wildcardHandlers = messageHandlers.get("*");
					if (wildcardHandlers) {
						wildcardHandlers.forEach((handler) => handler(data));
					}
				} catch (error) {
					console.error("Error parsing message:", error);
				}
			};

			ws.onclose = () => {
				if (mounted) setIsConnected(false);
				if (getSocket() === ws) {
					stopHeartbeat();
					setSocket(null);
				}
				// Reconnect through the currently mounted provider (if any), not
				// through this closure, so an unmounted provider never revives
				// the loop.
				scheduleReconnect();
			};

			ws.onerror = () => {
				if (mounted) setIsConnected(false);
				ws.close();
			};
		};

		// A fresh mount of the dashboard shell means the user is signed in:
		// re-arm reconnection that a previous logout switched off.
		setReconnectEnabled(true);
		setActiveConnect(() => {
			void connect();
		});
		void connect();

		// Cleanup function
		return () => {
			mounted = false;
			// Don't close the WebSocket (or its heartbeat) on unmount since it
			// is global, but make sure nothing reconnects on behalf of this
			// provider once it is gone.
			setActiveConnect(null);
			clearReconnectTimer();
			detachAdopted?.();
			detachAdopted = null;
		};
	}, [path]);

	return <WebSocketContext.Provider value={{ isConnected, ws: wsRef, subscribe, send }}>{children}</WebSocketContext.Provider>;
}

export function useWebSocket() {
	const context = useContext(WebSocketContext);
	if (!context) {
		throw new Error("useWebSocket must be used within a WebSocketProvider");
	}
	return context;
}
