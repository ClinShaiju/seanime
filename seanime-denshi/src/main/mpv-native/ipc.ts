import * as net from "node:net"
import { log } from "../logging"

export type MpvIpcEvent = { event: string } & Record<string, unknown>

type PendingRequest = {
    resolve: (value: unknown) => void
    reject: (error: Error) => void
}

const CONNECT_RETRY_MS = 100
const MAX_LINE_BYTES = 4 * 1024 * 1024

function delay(ms: number): Promise<void> {
    return new Promise(resolve => setTimeout(resolve, ms))
}

/**
 * Client for mpv's JSON IPC protocol (`--input-ipc-server`). Requests are newline-delimited JSON objects
 * carrying a `request_id`; mpv answers with the same id and pushes unsolicited `event` objects in between.
 */
export class MpvIpcClient {
    private socket: net.Socket | null = null
    private buffer = ""
    private nextRequestId = 1
    private readonly pending = new Map<number, PendingRequest>()
    private readonly eventListeners = new Set<(event: MpvIpcEvent) => void>()
    private closed = false

    get connected(): boolean {
        return !!this.socket && !this.closed
    }

    async connect(pipePath: string, timeoutMs: number): Promise<void> {
        const deadline = Date.now() + timeoutMs
        for (; ;) {
            if (this.closed) throw new Error("mpv ipc client was disposed before it connected")
            try {
                this.socket = await this.openSocket(pipePath)
                break
            }
            catch (error) {
                // mpv creates the pipe a moment after the process starts, so keep retrying until the deadline
                if (Date.now() >= deadline) throw error
                await delay(CONNECT_RETRY_MS)
            }
        }

        this.socket.setEncoding("utf8")
        this.socket.on("data", chunk => this.consume(String(chunk)))
        this.socket.on("error", error => log.warn(`[mpv-native] ipc socket error: ${error.message}`))
        this.socket.on("close", () => this.handleClose())
    }

    private openSocket(pipePath: string): Promise<net.Socket> {
        return new Promise((resolve, reject) => {
            const socket = net.connect(pipePath)
            const onError = (error: Error) => {
                socket.destroy()
                reject(error)
            }
            socket.once("error", onError)
            socket.once("connect", () => {
                socket.off("error", onError)
                resolve(socket)
            })
        })
    }

    onEvent(listener: (event: MpvIpcEvent) => void): () => void {
        this.eventListeners.add(listener)
        return () => this.eventListeners.delete(listener)
    }

    /** Sends a command and resolves with its `data` payload, or rejects with mpv's error string. */
    command(args: unknown[]): Promise<unknown> {
        const socket = this.socket
        if (!socket || this.closed) return Promise.reject(new Error("mpv ipc is not connected"))

        const requestId = this.nextRequestId++
        return new Promise((resolve, reject) => {
            this.pending.set(requestId, { resolve, reject })
            socket.write(`${JSON.stringify({ command: args, request_id: requestId })}\n`, error => {
                if (!error) return
                this.pending.delete(requestId)
                reject(error)
            })
        })
    }

    private consume(chunk: string): void {
        this.buffer += chunk
        for (; ;) {
            const newline = this.buffer.indexOf("\n")
            if (newline < 0) break
            const line = this.buffer.slice(0, newline).trim()
            this.buffer = this.buffer.slice(newline + 1)
            if (line) this.dispatch(line)
        }
        // A payload this large means the stream desynced; dropping it beats growing without bound
        if (this.buffer.length > MAX_LINE_BYTES) {
            log.warn("[mpv-native] dropping oversized ipc buffer")
            this.buffer = ""
        }
    }

    private dispatch(line: string): void {
        let message: any
        try {
            message = JSON.parse(line)
        }
        catch {
            log.warn(`[mpv-native] ignoring malformed ipc line: ${line.slice(0, 200)}`)
            return
        }

        if (typeof message?.request_id === "number") {
            const pending = this.pending.get(message.request_id)
            if (!pending) return
            this.pending.delete(message.request_id)
            if (message.error && message.error !== "success") pending.reject(new Error(String(message.error)))
            else pending.resolve(message.data)
            return
        }

        if (typeof message?.event === "string") {
            for (const listener of this.eventListeners) {
                try {
                    listener(message as MpvIpcEvent)
                }
                catch (error) {
                    log.warn(`[mpv-native] ipc event listener threw: ${error instanceof Error ? error.message : String(error)}`)
                }
            }
        }
    }

    private handleClose(): void {
        this.socket = null
        const error = new Error("mpv ipc connection closed")
        for (const pending of this.pending.values()) pending.reject(error)
        this.pending.clear()
    }

    dispose(): void {
        this.closed = true
        this.eventListeners.clear()
        const socket = this.socket
        this.handleClose()
        try {
            socket?.destroy()
        }
        catch {
            // already gone
        }
    }
}
