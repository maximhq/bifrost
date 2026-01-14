"use client";

import { getTokenFromStorage } from "@/lib/store/apis/baseApi";
import { getWebSocketUrl } from "@/lib/utils/port";
import React, { createContext, useCallback, useContext, useEffect, useRef, useState, type ReactNode } from "react";

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

// Global reference to maintain state across component remounts
let globalWsRef: WebSocket | null = null;
const messageHandlers = new Map<string, Set<MessageHandler>>();

export function WebSocketProvider({ children, path = "/ws" }: WebSocketProviderProps) {
	const wsRef = useRef<WebSocket | null>(globalWsRef);
	const reconnectTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);
	const pingTimerRef = useRef<ReturnType<typeof setInterval> | null>(null);
	const retryCountRef = useRef(0);
	const [isConnected, setIsConnected] = useState(false);

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
		if (wsRef.current?.readyState === WebSocket.OPEN) {
			try {
				wsRef.current.send(typeof data === "string" ? data : JSON.stringify(data));
			} catch (error) {
				console.error("Error sending message:", error);
			}
		}
	};

	useEffect(() => {
		const connect = async () => {
			if (wsRef.current?.readyState === WebSocket.OPEN) {
				return;
			}

			const wsUrl = getWebSocketUrl(path);
			const token = await getTokenFromStorage();
			const wsUrlWithAuth = token ? `${wsUrl}?token=${encodeURIComponent(token)}` : wsUrl;
			const ws = new WebSocket(wsUrlWithAuth);
			wsRef.current = ws;
			globalWsRef = ws;

			ws.onopen = () => {
				setIsConnected(true);
				retryCountRef.current = 0; // Reset retry count on successful connection

				// Clear any pending reconnection attempts
				if (reconnectTimeoutRef.current) {
					clearTimeout(reconnectTimeoutRef.current);
					reconnectTimeoutRef.current = null;
				}

				// Start heartbeat/ping to keep connection alive
				if (pingTimerRef.current) {
					clearInterval(pingTimerRef.current);
				}
				pingTimerRef.current = setInterval(() => {
					if (ws.readyState === WebSocket.OPEN) {
						try {
							ws.send("ping");
						} catch (error) {
							console.error("Error sending ping:", error);
						}
					}
				}, 25000); // Ping every 25 seconds
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
				setIsConnected(false);

				// Clear ping timer
				if (pingTimerRef.current) {
					clearInterval(pingTimerRef.current);
					pingTimerRef.current = null;
				}

				// Exponential backoff: 0.5s, 1s, 2s, 4s, 8s, 16s, 32s (max)
				retryCountRef.current = Math.min(retryCountRef.current + 1, 6);
				const delay = Math.pow(2, retryCountRef.current) * 500;
				
				reconnectTimeoutRef.current = setTimeout(connect, delay);
			};

			ws.onerror = (error) => {
				setIsConnected(false);
				ws.close();
			};
		};

		connect();

		// Cleanup function
		return () => {
			// Don't close the WebSocket on unmount since it's global
			if (reconnectTimeoutRef.current) {
				clearTimeout(reconnectTimeoutRef.current);
				reconnectTimeoutRef.current = null;
			}
			if (pingTimerRef.current) {
				clearInterval(pingTimerRef.current);
				pingTimerRef.current = null;
			}
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
