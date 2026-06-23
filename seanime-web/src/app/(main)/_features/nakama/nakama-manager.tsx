import {
    Nakama_NakamaStatus,
    Nakama_WatchRoom,
    VideoCore_OnlinestreamParams,
    VideoCore_ServerEvent,
} from "@/api/generated/types"
import {
    useNakamaCreateAndJoinRoom,
    useNakamaCreateWatchRoom,
    useNakamaDisconnectFromRoom,
    useNakamaJoinWatchRoom,
    useNakamaLeaveWatchRoom,
    useNakamaReconnectToHost,
    useNakamaRemoveStaleConnections,
    useNakamaRoomsAvailable,
    useNakamaSetWatchRoomAutoSkip,
    useNakamaSetWatchRoomControl,
    useNakamaSetWatchRoomForceTracks,
    useNakamaWatchRoomList,
} from "@/api/hooks/nakama.hooks"
import { useWebsocketMessageListener, useWebsocketSender } from "@/app/(main)/_hooks/handle-websockets"
import { useServerStatus } from "@/app/(main)/_hooks/use-server-status"
import { useNakamaOnlineStreamWatchParty } from "@/app/(main)/onlinestream/_lib/handle-onlinestream"
import { clientIdAtom, websocketConnectedAtom } from "@/app/websocket-provider"
import { ConfirmationDialog, useConfirmationDialog } from "@/components/shared/confirmation-dialog"
import { GlowingEffect } from "@/components/shared/glowing-effect"
import { SeaLink } from "@/components/shared/sea-link"
import { Badge } from "@/components/ui/badge"
import { Button, IconButton } from "@/components/ui/button"
import { cn } from "@/components/ui/core/styling"
import { LoadingSpinner } from "@/components/ui/loading-spinner"
import { Modal } from "@/components/ui/modal"
import { Switch } from "@/components/ui/switch"
import { TextInput } from "@/components/ui/text-input"
import { Tooltip } from "@/components/ui/tooltip"
import { copyToClipboard } from "@/lib/helpers/browser"
import { useWatchRoomPlayerSync } from "./nakama-room-sync"
import { WSEvents } from "@/lib/server/ws-events"
import { useThemeSettings } from "@/lib/theme/theme-hooks"
import { __isElectronDesktop__ } from "@/types/constants"
import { atom, useAtom, useAtomValue } from "jotai"
import React from "react"
import { BiCog } from "react-icons/bi"
import { FaBroadcastTower, FaLock } from "react-icons/fa"
import { HiOutlinePlay } from "react-icons/hi2"
import { LuClipboard, LuPopcorn } from "react-icons/lu"
import { MdAdd, MdCleaningServices, MdOutlineConnectWithoutContact, MdPlayArrow, MdRefresh } from "react-icons/md"
import { TbCloudPlus } from "react-icons/tb"
import { toast } from "sonner"
import { ElectronPlaybackMethod, useCurrentDevicePlaybackSettings } from "../../_atoms/playback.atoms"

export const nakamaModalOpenAtom = atom(false)
export const nakamaStatusAtom = atom<Nakama_NakamaStatus | null | undefined>(undefined)

// The same-instance watch room this client is currently in (null = not in a room).
// Lifted to an atom so the player can read it to emit control actions + apply incoming
// sync (the player-sync hook is the remaining wiring — see nakama-room.md).
export const currentWatchRoomAtom = atom<Nakama_WatchRoom | null>(null)

export function useNakamaStatus() {
    return useAtomValue(nakamaStatusAtom)
}

export function useNakamaWatchParty() {
    const nakamaStatus = useAtomValue(nakamaStatusAtom)
    const watchPartySession = React.useMemo(() => nakamaStatus?.currentWatchPartySession, [nakamaStatus])

    const currentUserPeerId = React.useMemo(() => {
        if (nakamaStatus?.isHost) {
            return "host"
        }
        return nakamaStatus?.hostConnectionStatus?.peerId || null
    }, [nakamaStatus])


    const isParticipant = React.useMemo(() => {
        if (!watchPartySession || !watchPartySession.participants) return false
        return nakamaStatus?.isHost || !!(currentUserPeerId && currentUserPeerId in watchPartySession.participants)
    }, [watchPartySession, nakamaStatus, currentUserPeerId])

    const isPeer = React.useMemo(() => {
        if (!isParticipant || currentUserPeerId === "host" || !currentUserPeerId || !watchPartySession) return false
        return !watchPartySession?.participants?.[currentUserPeerId]?.isRelayOrigin
    }, [watchPartySession])

    return {
        watchPartySession,
        isParticipant,
        isPeer: isPeer,
        isHost: currentUserPeerId === "host",
        currentUserPeerId,
    }
}

export function NakamaManager() {
    // Bridge the local player to the same-instance watch-room relay (emit/apply sync).
    useWatchRoomPlayerSync()

    const { sendMessage } = useWebsocketSender()
    const [isModalOpen, setIsModalOpen] = useAtom(nakamaModalOpenAtom)
    const [nakamaStatus, setNakamaStatus] = useAtom(nakamaStatusAtom)
    const clientId = useAtomValue(clientIdAtom)
    const ts = useThemeSettings()
    const serverStatus = useServerStatus()

    const roomInfo = nakamaStatus?.currentRoom

    const { mutate: reconnectToHost, isPending: isReconnecting } = useNakamaReconnectToHost()
    const { mutate: removeStaleConnections, isPending: isCleaningUp } = useNakamaRemoveStaleConnections()
    const { mutate: createAndJoinRoom, isPending: isCreatingRoom } = useNakamaCreateAndJoinRoom()
    const { mutate: disconnectFromRoom, isPending: isDisconnectingFromRoom } = useNakamaDisconnectFromRoom()
    const { data: roomsAvailable } = useNakamaRoomsAvailable()

    const { electronPlaybackMethod } = useCurrentDevicePlaybackSettings()

    function refetchStatus() {
        sendMessage({
            type: WSEvents.NAKAMA_STATUS_REQUESTED,
            payload: {
                // Tell the server whether this client is using the native player
                useDenshiPlayer: __isElectronDesktop__ && electronPlaybackMethod === ElectronPlaybackMethod.NativePlayer,
                clientId: clientId || "",
            },
        })
    }

    React.useEffect(() => {
        // When the playback method changes, update the status
        refetchStatus()
    }, [electronPlaybackMethod])

    useWebsocketMessageListener({
        type: WSEvents.NAKAMA_STATUS,
        onMessage: (data: Nakama_NakamaStatus | null) => {
            setNakamaStatus(data ?? null)
        },
    })

    // NAKAMA_WATCH_PARTY_STATE tells the client to refetch the status
    useWebsocketMessageListener({
        type: WSEvents.NAKAMA_WATCH_PARTY_STATE,
        onMessage: (data: any) => {
            refetchStatus()
        },
    })

    const websocketConnected = useAtomValue(websocketConnectedAtom)

    React.useEffect(() => {
        refetchStatus()
    }, [isModalOpen, websocketConnected])

    const handleReconnect = React.useCallback(() => {
        reconnectToHost({}, {
            onSuccess: () => {
                toast.success("Reconnection initiated")
                refetchStatus()
            },
            onError: (error) => {
                toast.error(`Failed to reconnect: ${error.message}`)
            },
        })
    }, [reconnectToHost, refetchStatus])

    const handleCleanupStaleConnections = React.useCallback(() => {
        removeStaleConnections({}, {
            onSuccess: () => {
                toast.success("Stale connections cleaned up")
                refetchStatus()
            },
            onError: (error) => {
                toast.error(`Failed to cleanup: ${error.message}`)
            },
        })
    }, [removeStaleConnections, refetchStatus])

    const handleCreateRoom = React.useCallback(() => {
        createAndJoinRoom(undefined, {
            onSuccess: () => {
                toast.success("Room created successfully")
                refetchStatus()
            },
        })
    }, [createAndJoinRoom, refetchStatus])

    const handleDisconnectFromRoom = React.useCallback(() => {
        disconnectFromRoom(undefined, {
            onSuccess: () => {
                toast.info("Disconnected from room")
                refetchStatus()
            },
            onError: (error) => {
                toast.error(`Failed to disconnect from room: ${error.message}`)
            },
        })
    }, [disconnectFromRoom, refetchStatus])

    useWebsocketMessageListener({
        type: WSEvents.NAKAMA_ROOM_CLOSED,
        onMessage: () => {
            refetchStatus()
        },
    })

    useWebsocketMessageListener({
        type: WSEvents.NAKAMA_ROOM_RECONNECTED,
        onMessage: (data: { roomId: string }) => {
            // toast.success("Reconnected to room")
            refetchStatus()
        },
    })

    useWebsocketMessageListener({
        type: WSEvents.NAKAMA_HOST_STARTED,
        onMessage: () => {
            refetchStatus()
        },
    })

    useWebsocketMessageListener({
        type: WSEvents.NAKAMA_HOST_STOPPED,
        onMessage: () => {
            refetchStatus()
        },
    })

    useWebsocketMessageListener({
        type: WSEvents.NAKAMA_PEER_CONNECTED,
        onMessage: () => {
            refetchStatus()
        },
    })

    useWebsocketMessageListener({
        type: WSEvents.NAKAMA_PEER_DISCONNECTED,
        onMessage: () => {
            refetchStatus()
        },
    })

    useWebsocketMessageListener({
        type: WSEvents.NAKAMA_HOST_CONNECTED,
        onMessage: () => {
            refetchStatus()
        },
    })

    useWebsocketMessageListener({
        type: WSEvents.NAKAMA_HOST_DISCONNECTED,
        onMessage: () => {
            refetchStatus()
        },
    })

    useWebsocketMessageListener({
        type: WSEvents.NAKAMA_ERROR,
        onMessage: () => {
            refetchStatus()
        },
    })

    /////// Online stream

    const { startOnlineStreamWatchParty } = useNakamaOnlineStreamWatchParty()
    useWebsocketMessageListener({
        type: WSEvents.VIDEOCORE,
        onMessage: ({ type, payload }: { type: VideoCore_ServerEvent, payload: unknown }) => {
            switch (type) {
                case "start-onlinestream-watch-party":
                    const data = payload as VideoCore_OnlinestreamParams
                    startOnlineStreamWatchParty(data)
            }
        },
    })

    const confirmRoom = useConfirmationDialog({
        title: "Create a Cloud Room",
        description: "By continuing, you agree to broadcast your playback state through Seanime's servers to sync with peers while the room is active. You are limited to 10 rooms per day and 4 peers per room (subject to change).",
        onConfirm: () => {
            handleCreateRoom()
        },
        actionIntent: "white-glass",
    })

    return <>
        <Modal
            open={isModalOpen}
            onOpenChange={setIsModalOpen}
            title={<div className="flex items-center gap-2 w-full justify-center">
                <MdOutlineConnectWithoutContact className="size-8" />
                Nakama
            </div>}
            contentClass={cn(
                "max-w-3xl bg-gray-950 bg-opacity-90 firefox:bg-opacity-100 firefox:backdrop-blur-none sm:rounded-3xl",
                ts.enableBlurringEffects && "bg-gray-950 bg-opacity-90 backdrop-blur-sm firefox:bg-opacity-100 firefox:backdrop-blur-none",
            )}
            overlayClass={cn(ts.enableBlurringEffects ? "bg-gray-950/70 backdrop-blur-sm" : "bg-black/80")}
            // allowOutsideInteraction
        >

            <GlowingEffect
                variant="classic"
                spread={40}
                glow={true}
                disabled={false}
                proximity={64}
                inactiveZone={0.01}
                className="opacity-50"
            />

            <div className="absolute top-4 right-14">
                <SeaLink href="/settings?tab=nakama" onClick={() => setIsModalOpen(false)}>
                    <IconButton intent="gray-basic" size="sm" icon={<BiCog />} />
                </SeaLink>
            </div>

            {nakamaStatus === undefined && <LoadingSpinner />}

            {/* Same-instance watch rooms — available to every user, independent of the
                host/peer federation below. */}
            {nakamaStatus !== undefined && <WatchRoomsSection open={isModalOpen} />}

            {!nakamaStatus?.isHost && (
                <div className="flex items-center justify-between">
                    <div></div>
                    <Button
                        onClick={handleReconnect}
                        disabled={isReconnecting}
                        size="sm"
                        intent="gray-basic"
                        leftIcon={<MdRefresh />}
                    >
                        {isReconnecting ? "Reconnecting..." : "Reconnect"}
                    </Button>
                </div>
            )}

            {nakamaStatus !== undefined && (nakamaStatus?.isHost || nakamaStatus?.isConnectedToHost) && (
                <>

                    {nakamaStatus?.isHost && (
                        <>
                            <div className="flex items-center justify-between">
                                <Badge intent="success-solid" className="px-0 text-indigo-300 bg-transparent">Currently hosting</Badge>
                                <Button
                                    onClick={handleCleanupStaleConnections}
                                    disabled={isCleaningUp}
                                    size="sm"
                                    intent="gray-basic"
                                    leftIcon={<MdCleaningServices />}
                                >
                                    {isCleaningUp ? "Cleaning up..." : "Remove stale connections"}
                                </Button>
                            </div>

                            {/* Cloud Rooms */}
                            {nakamaStatus?.connectionMode === "rooms" && roomInfo
                                ? (
                                    <div className="space-y-2">
                                        <div className="flex items-center justify-between">
                                            <h4>Cloud Room</h4>
                                            <Button
                                                onClick={handleDisconnectFromRoom}
                                                disabled={isDisconnectingFromRoom}
                                                size="sm"
                                                intent="alert-link"
                                            >
                                                {isDisconnectingFromRoom ? "Disconnecting..." : "Disconnect"}
                                            </Button>
                                        </div>
                                        <p className="text-sm text-[--muted]">
                                            Cloud Rooms do not support local file and debrid playback.
                                        </p>
                                        <div className="p-4 border rounded-lg bg-gray-950 space-y-3">
                                            <div className="space-y-1">
                                                <span className="text-sm text-[--muted]">Nakama Host URL and Passcode</span>
                                                <div className="flex items-center gap-2">
                                                    <TextInput
                                                        readOnly
                                                        leftAddon="Host URL"
                                                        value={`room://${roomInfo.roomId}`}
                                                        onClick={(e) => e.currentTarget.select()}
                                                        addonClass="font-bold tracking-wide text-sm pr-2"
                                                        rightAddon={<>
                                                            <IconButton
                                                                size="sm"
                                                                intent="gray-basic"
                                                                onClick={() => {
                                                                    copyToClipboard(`room://${roomInfo.roomId}`)
                                                                        .then(() => toast.success("Copied to clipboard"))
                                                                }}
                                                                icon={<LuClipboard />}
                                                            />
                                                        </>}
                                                    />
                                                </div>
                                                <div className="flex items-center gap-2">
                                                    <TextInput
                                                        readOnly
                                                        leftAddon="Passcode"
                                                        value={serverStatus?.settings?.nakama?.hostPassword || "No password set"}
                                                        onClick={(e) => e.currentTarget.select()}
                                                        addonClass="font-bold tracking-wide text-sm pr-2"
                                                        rightAddon={<>
                                                            <IconButton
                                                                size="sm"
                                                                intent="gray-basic"
                                                                onClick={() => {
                                                                    copyToClipboard(serverStatus?.settings?.nakama?.hostPassword || "")
                                                                        .then(() => toast.success("Copied to clipboard"))
                                                                }}
                                                                icon={<LuClipboard />}
                                                            />
                                                        </>}
                                                    />
                                                </div>
                                            </div>
                                            {roomInfo.expiresAt && <div className="flex items-center gap-1">
                                                <span className="text-sm text-[--muted]">Expires: </span>
                                                <span className="text-sm font-semibold">{new Date(roomInfo.expiresAt).toLocaleString()}</span>
                                            </div>}
                                        </div>
                                    </div>
                                )
                                : nakamaStatus?.connectionMode === "direct" && roomsAvailable && (!nakamaStatus?.currentWatchPartySession || (nakamaStatus.currentWatchPartySession && nakamaStatus.currentWatchPartySession?.isRoom)) && (
                                <div className="space-y-2">
                                    <div className="p-4 border rounded-lg bg-gray-950">
                                        <div className="flex items-center justify-between">
                                            <div className="space-y-1">
                                                <p className="font-bold">
                                                    Cloud Room
                                                </p>
                                                <p className="text-sm text-[--muted] pr-4">
                                                    Cloud Rooms use Seanime's API to enable hosting watch parties without exposing your server to the
                                                    internet.
                                                </p>
                                            </div>
                                            <Tooltip
                                                trigger={<Button
                                                    onClick={confirmRoom.open}
                                                    disabled={isCreatingRoom}
                                                    size="sm"
                                                    intent="white-glass"
                                                    leftIcon={<TbCloudPlus className="text-2xl" />}
                                                >
                                                    {isCreatingRoom ? "Creating..." : "Create a Cloud Room"}
                                                </Button>}
                                            >
                                                You will automatically join the room.
                                            </Tooltip>
                                        </div>
                                    </div>
                                </div>
                            )}

                            {/* External (separate-instance) peers — only shown when some are connected. */}
                            {nakamaStatus.connectionMode === "direct" && !!nakamaStatus?.connectedPeers?.length && <>
                                <h4>External connections ({nakamaStatus.connectedPeers.length})</h4>
                                <div className="p-4 border rounded-lg bg-gray-950">
                                    {nakamaStatus.connectedPeers.map((peer, index) => (
                                        <div key={index} className="flex items-center justify-between py-1">
                                            <span className="font-medium">{peer}</span>
                                        </div>
                                    ))}
                                </div>
                            </>}
                        </>
                    )}

                    {(nakamaStatus?.isConnectedToHost && !nakamaStatus?.isHost) && (
                        <>

                            <h4>Host connection</h4>
                            <div className="p-4 border rounded-lg bg-gray-950">
                                <div className="space-y-2">
                                    <div className="flex items-center justify-between">
                                        <span className="text-sm text-[--muted]">Host</span>
                                        <span className="font-medium text-sm tracking-wide">
                                            {nakamaStatus?.hostConnectionStatus?.username || "Unknown"}
                                        </span>
                                    </div>
                                    <div className="flex items-center justify-between">
                                        <span className="text-sm text-[--muted]">Connection Mode</span>
                                        <Badge intent={nakamaStatus?.hostConnectionStatus?.connectionMode === "rooms" ? "primary" : "gray"}>
                                            {nakamaStatus?.hostConnectionStatus?.connectionMode === "rooms" ? "Cloud Room" : "Direct"}
                                        </Badge>
                                    </div>
                                </div>
                            </div>
                        </>
                    )}

                    {/* Watch Party (legacy peer/host) removed — Watch Rooms (top) replaces it. */}
                </>
            )}

            {!nakamaStatus?.isHost && !nakamaStatus?.isConnectedToHost && nakamaStatus !== undefined && (
                <div className="text-center py-8">
                    <p className="text-[--muted]">Nakama is not active</p>
                    <p className="text-sm text-[--muted] mt-2">
                        Configure Nakama in settings to connect to a host or start hosting
                    </p>
                </div>
            )}
        </Modal>

        <ConfirmationDialog {...confirmRoom} />
    </>
}

// WatchRoomsSection renders the same-instance watch rooms: discovery cards + create/join,
// and the in-room member list + host control panel. Independent of the host/peer party.
// Identity is resolved server-side; "me" is matched by this client's id in the room.
function WatchRoomsSection({ open }: { open: boolean }) {
    const clientId = useAtomValue(clientIdAtom)

    const { data: rooms, refetch: refetchRooms } = useNakamaWatchRoomList()
    const { mutate: createRoom, isPending: isCreating } = useNakamaCreateWatchRoom()
    const { mutate: joinRoom, isPending: isJoining } = useNakamaJoinWatchRoom()
    const { mutate: leaveRoom, isPending: isLeaving } = useNakamaLeaveWatchRoom()
    const { mutate: setControl } = useNakamaSetWatchRoomControl()
    const { mutate: setForceTracks } = useNakamaSetWatchRoomForceTracks()
    const { mutate: setAutoSkip } = useNakamaSetWatchRoomAutoSkip()

    const [currentRoom, setCurrentRoom] = useAtom(currentWatchRoomAtom)
    const [showCreate, setShowCreate] = React.useState(false)
    const [newName, setNewName] = React.useState("")
    const [newPassword, setNewPassword] = React.useState("")
    const [joinTarget, setJoinTarget] = React.useState<string | null>(null)
    const [joinPassword, setJoinPassword] = React.useState("")

    // Refresh the discovery list whenever rooms change.
    useWebsocketMessageListener({
        type: WSEvents.NAKAMA_ROOMS_UPDATED,
        onMessage: () => refetchRooms(),
    })

    // Track the room we're in (server pushes state to its members only).
    useWebsocketMessageListener({
        type: WSEvents.NAKAMA_WATCH_ROOM_STATE,
        onMessage: (room: Nakama_WatchRoom | null) => {
            setCurrentRoom(prev => (room && prev && prev.id === room.id) ? room : prev)
        },
    })

    React.useEffect(() => {
        if (open) refetchRooms()
    }, [open])

    // "Me" = the participant whose driving client is this client.
    const me = React.useMemo(() => {
        if (!currentRoom?.participants) return null
        const entry = Object.entries(currentRoom.participants).find(([, p]) => p.clientId === clientId)
        return entry ? { key: entry[0], participant: entry[1] } : null
    }, [currentRoom, clientId])

    const amHost = !!me?.participant?.isHost

    function handleCreate() {
        if (!newName.trim()) return
        createRoom({ name: newName.trim(), password: newPassword, clientId: clientId || "" }, {
            onSuccess: (room) => {
                setCurrentRoom(room ?? null)
                setShowCreate(false)
                setNewName("")
                setNewPassword("")
                refetchRooms()
            },
            onError: (e) => toast.error(e.message),
        })
    }

    function handleJoin(roomId: string, password: string) {
        joinRoom({ roomId, password, clientId: clientId || "" }, {
            onSuccess: (room) => {
                setCurrentRoom(room ?? null)
                setJoinTarget(null)
                setJoinPassword("")
                refetchRooms()
            },
            onError: (e) => toast.error(e.message),
        })
    }

    function handleLeave() {
        if (!currentRoom) return
        leaveRoom({ roomId: currentRoom.id }, {
            onSuccess: () => {
                setCurrentRoom(null)
                refetchRooms()
            },
        })
    }

    function handleSetControl(targetKey: string, canControl: boolean, all: boolean) {
        if (!currentRoom) return
        setControl({ roomId: currentRoom.id, targetKey, canControl, all }, {
            onError: (e) => toast.error(e.message),
        })
    }

    // ---- In a room ----
    if (currentRoom) {
        const participants = Object.entries(currentRoom.participants || {})
        return (
            <div className="space-y-3">
                <div className="flex items-center justify-between">
                    <h4 className="flex items-center gap-2"><LuPopcorn className="size-6" /> {currentRoom.name}</h4>
                    <Button onClick={handleLeave} disabled={isLeaving} size="sm" intent="alert-basic">
                        {isLeaving ? "Leaving..." : amHost ? "Close room" : "Leave"}
                    </Button>
                </div>

                <h5>Members ({participants.length})</h5>
                <div className="p-4 border rounded-lg bg-gray-950 space-y-1">
                    {participants.map(([key, p]) => {
                        const isMe = p.clientId === clientId
                        const isController = key === currentRoom.controllerKey
                        return (
                            <div key={key} className="flex items-center justify-between py-1">
                                <div className="flex items-center gap-2">
                                    <span className="font-medium text-sm tracking-wide">
                                        {p.user?.username}
                                        {isMe && <span className="text-[--muted] font-normal"> (me)</span>}
                                    </span>
                                    {p.isHost && <Badge className="text-xs">Host</Badge>}
                                    {isController && !p.isHost && <Badge intent="warning" className="text-xs">Controlling</Badge>}
                                    {!p.isHost && p.canControl && <Badge intent="success" className="text-xs">Can control</Badge>}
                                </div>
                                {amHost && !p.isHost && (
                                    <Tooltip trigger={<Switch
                                        value={!!p.canControl}
                                        onValueChange={(v) => handleSetControl(key, v, false)}
                                    />}>
                                        Allow this member to control playback
                                    </Tooltip>
                                )}
                            </div>
                        )
                    })}
                </div>

                {amHost && participants.length > 1 && (
                    <div className="flex items-center justify-between p-3 border rounded-lg bg-gray-950">
                        <span className="text-sm">Let everyone control playback</span>
                        <div className="flex items-center gap-2">
                            <Button size="sm" intent="primary-subtle" onClick={() => handleSetControl("", true, true)}>Everyone</Button>
                            <Button size="sm" intent="gray-basic" onClick={() => handleSetControl("", false, true)}>Host only</Button>
                        </div>
                    </div>
                )}

                {amHost && (
                    <div className="flex items-center justify-between p-3 border rounded-lg bg-gray-950">
                        <div>
                            <p className="text-sm">Force my subtitle &amp; audio tracks</p>
                            <p className="text-xs text-[--muted]">Off: everyone picks their own.</p>
                        </div>
                        <Switch
                            value={!!currentRoom.forceHostTracks}
                            onValueChange={(v) => setForceTracks({ roomId: currentRoom.id, forceHostTracks: v }, {
                                onError: (e) => toast.error(e.message),
                            })}
                        />
                    </div>
                )}

                {/* Auto-skip OP/ED vote */}
                <div className="p-3 border rounded-lg bg-gray-950 space-y-2">
                    <div className="flex items-center justify-between">
                        <span className="text-sm">Auto-skip OP/ED</span>
                        <span className="text-xs">
                            <span className={currentRoom.effectiveAutoSkip ? "text-green-400" : "text-[--muted]"}>
                                {currentRoom.effectiveAutoSkip ? "On" : "Off"}
                            </span>
                            <span className="text-[--muted]"> · {currentRoom.autoSkipVotesOn} on / {currentRoom.autoSkipVotesOff} off</span>
                        </span>
                    </div>
                    <div className="flex items-center gap-1">
                        {(["on", "off", "auto"] as const).map(opt => {
                            const active = me?.participant?.autoSkipPref === opt
                            return (
                                <Button
                                    key={opt}
                                    size="sm"
                                    intent={active ? "primary" : "gray-basic"}
                                    className="capitalize flex-1"
                                    onClick={() => setAutoSkip({ roomId: currentRoom.id, pref: opt }, {
                                        onError: (e) => toast.error(e.message),
                                    })}
                                >
                                    {opt}
                                </Button>
                            )
                        })}
                    </div>
                    <p className="text-xs text-[--muted]">
                        Your vote. &quot;Auto&quot; follows the majority. Only the controller skips; everyone else follows the synced seek.
                    </p>
                </div>

                <p className="text-xs text-[--muted]">
                    {currentRoom.forceHostTracks
                        ? "The host's subtitle and audio track are applied to everyone."
                        : "Everyone keeps their own subtitles and audio track."} Start the same episode to sync.
                </p>
            </div>
        )
    }

    // ---- Discovery ----
    return (
        <div className="space-y-3">
            <div className="flex items-center justify-between">
                <h4 className="flex items-center gap-2"><LuPopcorn className="size-6" /> Watch Rooms</h4>
                <Button size="sm" intent="primary-subtle" leftIcon={<MdAdd />} onClick={() => setShowCreate(s => !s)}>
                    Create room
                </Button>
            </div>

            {showCreate && (
                <div className="p-4 border rounded-lg bg-gray-950 space-y-2">
                    <TextInput placeholder="Room name" value={newName} onValueChange={setNewName} />
                    <TextInput type="password" placeholder="Password (optional)" value={newPassword} onValueChange={setNewPassword} />
                    <Button className="w-full" intent="primary" disabled={isCreating || !newName.trim()} onClick={handleCreate}>
                        {isCreating ? "Creating..." : "Create & join"}
                    </Button>
                </div>
            )}

            {!rooms?.length && !showCreate && (
                <div className="text-center py-6 text-[--muted] text-sm">
                    No active rooms. Create one to start watching together.
                </div>
            )}

            <div className="space-y-2">
                {rooms?.map((room) => (
                    <div key={room.id} className="relative p-3 border rounded-lg bg-gray-950 flex gap-3">
                        {room.coverImage && (
                            <img src={room.coverImage} alt="" className="w-12 h-16 object-cover rounded-md shrink-0" />
                        )}
                        <div className="flex-1 min-w-0">
                            <div className="flex items-start justify-between gap-2">
                                <p className="font-semibold truncate">{room.name}</p>
                                {room.hasPassword && (
                                    <Tooltip trigger={<FaLock className="text-yellow-400 shrink-0 mt-1" />}>
                                        Password required
                                    </Tooltip>
                                )}
                            </div>
                            <p className="text-sm text-[--muted]">Host: {room.hostUsername || "Unknown"}</p>
                            {room.title && (
                                <p className="text-xs text-[--muted] truncate">
                                    {room.title}{room.episodeNumber ? ` · E${room.episodeNumber}` : ""}
                                </p>
                            )}
                            <div className="flex items-center justify-between mt-2">
                                <span className="text-xs text-[--muted]">
                                    {room.memberCount} {room.memberCount === 1 ? "member" : "members"}
                                </span>
                                {joinTarget === room.id ? (
                                    <div className="flex items-center gap-1">
                                        <TextInput
                                            type="password"
                                            placeholder="Password"
                                            value={joinPassword}
                                            onValueChange={setJoinPassword}
                                        />
                                        <Button size="sm" intent="primary" disabled={isJoining} onClick={() => handleJoin(room.id, joinPassword)}>
                                            Join
                                        </Button>
                                    </div>
                                ) : (
                                    <Button
                                        size="sm"
                                        intent="primary-subtle"
                                        disabled={isJoining}
                                        onClick={() => room.hasPassword ? setJoinTarget(room.id) : handleJoin(room.id, "")}
                                    >
                                        Join
                                    </Button>
                                )}
                            </div>
                        </div>
                    </div>
                ))}
            </div>
        </div>
    )
}
