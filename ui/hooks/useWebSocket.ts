import { useEffect, useRef, useState } from "react";
import type { LogEntry } from "@/lib/types/logs";

interface WebSocketHookProps {
	onMessage: (log: LogEntry) => void;
}

declare const process: {
	env: {
		NEXT_PUBLIC_BIFROST_PORT?: string;
	};
};

export function useWebSocket({ onMessage }: WebSocketHookProps) {
	const wsRef = useRef<WebSocket | null>(null);
	const reconnectTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);
	const [isConnected, setIsConnected] = useState(false);

	useEffect(() => {
		const port = process.env.NEXT_PUBLIC_BIFROST_PORT || "8080";
		const connect = () => {
			if (wsRef.current?.readyState === WebSocket.OPEN) {
				return;
			}

			const ws = new WebSocket(`ws://localhost:${port}/ws/logs`);
			wsRef.current = ws;

			ws.onopen = () => {
				console.log("WebSocket connected");
				setIsConnected(true);
				// Clear any pending reconnection attempts
				if (reconnectTimeoutRef.current) {
					clearTimeout(reconnectTimeoutRef.current);
					reconnectTimeoutRef.current = null;
				}
			};

			ws.onmessage = (event) => {
				try {
					const data = JSON.parse(event.data);
					if (data.type === "log") {
						onMessage(data.payload);
					}
				} catch (error) {
					console.error("Failed to parse WebSocket message:", error);
				}
			};

			ws.onclose = () => {
				console.log("WebSocket disconnected, attempting to reconnect...");
				setIsConnected(false);
				// Attempt to reconnect after 5 seconds
				reconnectTimeoutRef.current = setTimeout(connect, 5000);
			};

			ws.onerror = (error) => {
				console.error("WebSocket error:", error);
				setIsConnected(false);
				ws.close();
			};
		};

		connect();

		// Cleanup function
		return () => {
			if (wsRef.current) {
				wsRef.current.close();
				wsRef.current = null;
			}
			if (reconnectTimeoutRef.current) {
				clearTimeout(reconnectTimeoutRef.current);
				reconnectTimeoutRef.current = null;
			}
			setIsConnected(false);
		};
	}, [onMessage]); // Add onMessage to dependencies to avoid stale closure

	return { ws: wsRef, isConnected };
}
