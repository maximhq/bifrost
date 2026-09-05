import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
	HEARTBEAT_INTERVAL_MS,
	adoptSocket,
	getSocket,
	isReconnectEnabled,
	setActiveConnect,
	setReconnectEnabled,
	setSocket,
	scheduleReconnect,
	startHeartbeat,
	stopHeartbeat,
	stopWebSocket,
} from "./useWebSocket.utils";

const CONNECTING = 0;
const OPEN = 1;
const CLOSED = 3;

/** Minimal stand-in for a browser WebSocket: readyState, send, close and the two event APIs the lifecycle uses. */
class FakeSocket {
	readyState = CONNECTING;
	sent: string[] = [];
	onclose: (() => void) | null = null;
	onerror: (() => void) | null = null;
	private listeners = new Map<string, Set<() => void>>();

	addEventListener(type: string, fn: () => void) {
		if (!this.listeners.has(type)) this.listeners.set(type, new Set());
		this.listeners.get(type)!.add(fn);
	}
	removeEventListener(type: string, fn: () => void) {
		this.listeners.get(type)?.delete(fn);
	}
	listenerCount(type: string) {
		return this.listeners.get(type)?.size ?? 0;
	}
	send(data: string) {
		this.sent.push(data);
	}
	close() {
		this.readyState = CLOSED;
		this.emit("close");
		this.onclose?.();
	}
	open() {
		this.readyState = OPEN;
		this.emit("open");
	}
	private emit(type: string) {
		this.listeners.get(type)?.forEach((fn) => fn());
	}
}

const asWs = (s: FakeSocket) => s as unknown as WebSocket;

describe("useWebSocket lifecycle", () => {
	beforeEach(() => {
		vi.useFakeTimers();
		stopWebSocket();
		setReconnectEnabled(true);
		setActiveConnect(null);
	});
	afterEach(() => {
		stopWebSocket();
		vi.useRealTimers();
	});

	describe("adoptSocket", () => {
		it("reports open to the adopting provider when it adopts a CONNECTING socket", () => {
			// The provider that created the socket may already be unmounted, so its
			// onopen closure cannot update the new provider's state.
			const socket = new FakeSocket();
			setSocket(asWs(socket));
			const onOpen = vi.fn();
			const onClose = vi.fn();

			adoptSocket(asWs(socket), { onOpen, onClose });
			expect(onOpen).not.toHaveBeenCalled();

			socket.open();
			expect(onOpen).toHaveBeenCalledTimes(1);
			expect(onClose).not.toHaveBeenCalled();
		});

		it("reports close to the adopting provider", () => {
			const socket = new FakeSocket();
			socket.readyState = OPEN;
			setSocket(asWs(socket));
			const onClose = vi.fn();

			adoptSocket(asWs(socket), { onOpen: vi.fn(), onClose });
			socket.close();
			expect(onClose).toHaveBeenCalledTimes(1);
		});

		it("restarts the heartbeat when adopting an already-open socket that has none", () => {
			const socket = new FakeSocket();
			socket.readyState = OPEN;
			setSocket(asWs(socket));
			stopHeartbeat();

			adoptSocket(asWs(socket), { onOpen: vi.fn(), onClose: vi.fn() });
			vi.advanceTimersByTime(HEARTBEAT_INTERVAL_MS);
			expect(socket.sent).toEqual(["ping"]);
		});

		it("detaches its listeners when the adopting provider unmounts", () => {
			const socket = new FakeSocket();
			setSocket(asWs(socket));
			const onOpen = vi.fn();

			const detach = adoptSocket(asWs(socket), { onOpen, onClose: vi.fn() });
			expect(socket.listenerCount("open")).toBe(1);
			detach();
			expect(socket.listenerCount("open")).toBe(0);
			expect(socket.listenerCount("close")).toBe(0);

			socket.open();
			expect(onOpen).not.toHaveBeenCalled();
		});
	});

	describe("heartbeat", () => {
		it("keeps pinging on the shared socket independent of any provider mount", () => {
			const socket = new FakeSocket();
			socket.readyState = OPEN;
			setSocket(asWs(socket));

			startHeartbeat(asWs(socket));
			vi.advanceTimersByTime(HEARTBEAT_INTERVAL_MS * 2);
			expect(socket.sent).toEqual(["ping", "ping"]);
		});

		it("does not ping a socket that is not open", () => {
			const socket = new FakeSocket();
			setSocket(asWs(socket));

			startHeartbeat(asWs(socket));
			vi.advanceTimersByTime(HEARTBEAT_INTERVAL_MS);
			expect(socket.sent).toEqual([]);
		});

		it("replaces a previous heartbeat instead of stacking a second one", () => {
			const socket = new FakeSocket();
			socket.readyState = OPEN;
			setSocket(asWs(socket));

			startHeartbeat(asWs(socket));
			startHeartbeat(asWs(socket));
			vi.advanceTimersByTime(HEARTBEAT_INTERVAL_MS);
			expect(socket.sent).toEqual(["ping"]);
		});
	});

	describe("stopWebSocket", () => {
		it("stops the heartbeat, closes the socket without reconnecting, and disables reconnection", () => {
			const socket = new FakeSocket();
			socket.readyState = OPEN;
			setSocket(asWs(socket));
			startHeartbeat(asWs(socket));
			const connect = vi.fn();
			setActiveConnect(connect);
			socket.onclose = () => scheduleReconnect();

			stopWebSocket();

			expect(getSocket()).toBeNull();
			expect(socket.readyState).toBe(CLOSED);
			expect(isReconnectEnabled()).toBe(false);
			vi.advanceTimersByTime(HEARTBEAT_INTERVAL_MS * 4);
			expect(socket.sent).toEqual([]);
			expect(connect).not.toHaveBeenCalled();
		});
	});

	describe("scheduleReconnect", () => {
		it("reconnects through the active provider with backoff", () => {
			const connect = vi.fn();
			setActiveConnect(connect);

			scheduleReconnect();
			vi.advanceTimersByTime(999);
			expect(connect).not.toHaveBeenCalled();
			vi.advanceTimersByTime(1);
			expect(connect).toHaveBeenCalledTimes(1);
		});

		it("does nothing once no provider is mounted", () => {
			const connect = vi.fn();
			setActiveConnect(connect);
			setActiveConnect(null);

			scheduleReconnect();
			vi.advanceTimersByTime(60_000);
			expect(connect).not.toHaveBeenCalled();
		});
	});
});
