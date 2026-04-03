# Cowork Service Binary Analysis — v1.569.0

## Binary Overview

- **Windows**: cowork-svc.exe — Go binary (~11 MB), implements Hyper-V VM management
- **macOS**: cowork-svc — Go binary (~4.5 MB), implements Apple Virtualization framework
- Both bundled inside Claude Desktop installer at `lib/net45/` level

## Extracted Files (bin/ directory)

The extract script pulls all files from the same directory level as cowork-svc.exe:

| File | Size | Purpose |
|------|------|---------|
| cowork-svc.exe | 11 MB | Windows Hyper-V backend (Go binary) |
| app.asar | 19 MB | Claude Desktop Electron app (same as main app) |
| chrome-native-host.exe | 1 MB | Chrome native messaging host for browser tools |
| cowork-plugin-shim.sh | 7.5 KB | Plugin permission gating library (new in v1.1.9669, updated in v1.2.234) |
| smol-bin.x64.vhdx | 36 MB | Empty ext4 filesystem for sdk-daemon updater |
| default.clod | 97 KB | Default configuration/data |
| *.json (locale files) | ~15-75 KB each | UI translations (de-DE, en-US, es-419, etc.) |
| *.png / *.ico | ~2-4 KB each | Tray icons (light/dark, various DPI) |
| .version | 8 bytes | Version string ("1.2.234") |

## Windows Architecture

```
Claude Desktop (Electron)
  -> Named Pipe (\\.\pipe\cowork-vm-service)
    -> cowork-svc.exe (Go)
      -> Hyper-V API
        -> Linux VM (rootfs.vhdx + vmlinuz + initrd)
          -> sdk-daemon (vsock, port 51234/0xC822)
            -> Claude Code CLI
```

## macOS Architecture

```
Claude Desktop (Electron)
  -> Unix Socket
    -> cowork-svc (Go, Swift bindings)
      -> Apple Virtualization.framework
        -> Linux VM (rootfs.img)
          -> sdk-daemon (vsock)
            -> Claude Code CLI
```

## Linux Native Architecture (Our Implementation)

```
Claude Desktop (Electron, patched)
  -> Unix Socket ($XDG_RUNTIME_DIR/cowork-vm-service.sock)
    -> cowork-svc-linux (Go, this project)
      -> Direct host execution (os/exec)
        -> Claude Code CLI
```

## Protocol Differences Between Platforms

| Aspect | Windows | macOS | Linux (ours) |
|--------|---------|-------|-------------|
| Transport | Named Pipe | Unix Socket | Unix Socket |
| VM | Hyper-V | Apple Virtualization | None (native) |
| Guest comms | HVSocket (AF_HYPERV) | vsock (AF_VSOCK) | N/A (direct exec) |
| vsock port | 0xC822 (51234) | 0xC822 (51234) | N/A |
| Binary | cowork-svc.exe (Go) | cowork-svc (Go+Swift) | cowork-svc-linux (Go) |
| Bundle | rootfs.vhdx + vmlinuz + initrd | rootfs.img | None needed |

---

## cowork-svc.exe Deep Analysis (v1.569.0)

| Property | Value |
|----------|-------|
| **File type** | PE32+ executable for MS Windows 6.01 (console), x86-64, 8 sections |
| **Go version** | go1.24.13 |
| **Module** | github.com/anthropics/cowork-win32-service |
| **Build date** | 2026-04-03 |
| **Size** | 11,186,000 bytes |
| **SHA256** | a31c0aed813d1a8384c31e6fdbcc0fc4b48c9c4d5ebefe659dea274a06101387 |

### Go Module Structure (from binary strings)

Three packages: `main`, `pipe`, `vm`

#### pipe package (RPC protocol handling)

**Server lifecycle:**
- `pipe.NewServer`, `pipe.(*Server).Start`, `pipe.(*Server).Stop`
- `pipe.(*Server).acceptLoop`, `pipe.(*Server).handleConnection`

**Request dispatch:**
- `pipe.(*Server).dispatch`, `pipe.(*Server).dispatchVerified`, `pipe.(*Server).dispatchWithSession`

**Session management:**
- `pipe.(*Server).getOrCreateSession`, `pipe.(*Server).getSessionForConn`
- `pipe.(*Server).checkIdleSessions`, `pipe.(*Server).idleSessionChecker`
- `pipe.(*vmSession).broadcast`, `pipe.(*vmSession).isConfigured`, `pipe.(*vmSession).subscriberCount`

**RPC handlers:**
- handleConfigure
- handleCreateVM
- handleStartVM
- handleStopVM
- handleSubscription
- handleWriteStdin
- handleIsRunning
- handleIsGuestConnected
- handleIsProcessRunning
- handleIsDebugLoggingEnabled
- handleSetDebugLogging
- handleCreateDiskImage
- handleSendGuestResponse *(new in v1.569.0)*
- handlePassthrough
- handlePersistentRPC

**Wire protocol:**
- `pipe.ReadMessage`, `pipe.WriteMessage`

**Windows security:**
- `pipe.(*Server).InitSignatureVerification`, `pipe.(*Server).verifyClientSignature` — code signing verification
- `pipe.calculateCertThumbprint`, `pipe.getSigningCertificateInfo` — Windows code signing
- `pipe.GetClientInfo`, `pipe.GetClientInfoFromConn` — caller authentication
- `pipe.getPackageFamilyName` — UWP/MSIX package identity
- `pipe.getUserProfileDirectory`, `pipe.lookupSID` — Windows user identity

#### vm package (Hyper-V management)

**VM lifecycle (`vm.(*WindowsVMManager)`):**
- CreateVM, StartVM, StartVMWithBundle, StopVM
- IsRunning, IsGuestConnected, IsProcessRunning

**Filesystem sharing:**
- AddPlan9Share — 9P filesystem sharing (host -> VM)

**Process management:**
- ForwardToVM, WriteStdin

**VM configuration:**
- SetMemoryMB, SetCPUCount, SetKernelPath, SetInitrdPath, SetVHDXPath
- SetSmolBinPath, SetSessionDiskPath, SetCondaDiskPath — disk management
- SetUserToken, SetOwner — Windows user context
- SetEventCallbacks, emitStartupStep

**TLS/CA:**
- installHostCACertificates — TLS CA injection
- `vm.LoadTrustedCACertificates` — host CA cert loading

**HCS (Host Compute Service) API:**
- `vm.CreateComputeSystem`, `vm.OpenComputeSystem`, `vm.EnumerateComputeSystems`
- `vm.(*HCSSystem)` — Start, Shutdown, Terminate, Close, GetProperties, ModifyComputeSystem, AddPlan9Share
- `vm.(*VMConfig).BuildHCSDocument` — HCS configuration generation

**vsock RPC to sdk-daemon (`vm.(*RPCServer)`):**
- acceptLoop, handleConnection, handleMessage, handleEvent, handleResponse
- SendRequestAndWait, SendNotification, SendInstallCACertificates, writeFrame
- IsConnected, SetCallbacks, Start, Stop

**Hyper-V sockets:**
- `vm.(*HVSocketListener)`, `vm.(*HVSocketConn)` — AF_HYPERV socket types

**Console/networking:**
- `vm.(*ConsoleReader)` — VM console output capture
- `vm.(*VirtualNetworkProvider)` — HCN networking

**VM lifecycle utilities:**
- `vm.CleanupStaleVMs`, `vm.VMIDForSID`, `vm.isOurVM`
- `vm.CreateSparseVHDX` — dynamic disk creation
- `vm.VsockPortToServiceGUID`, `vm.NetworkVsockServiceGUID` — GUID mapping

**Path security:**
- `vm.ValidateWritePath`, `vm.validateLogPath`

### External Dependencies

- `github.com/apparentlymart/go-cidr/cidr` — CIDR arithmetic for networking
- `github.com/containers/gvisor-tap-vsock` — gVisor networking (DHCP, DNS, forwarder)
- `golang.org/x/net/http2` — HTTP/2 support

### Notable Methods Not in Our Handler

| Method | Purpose | Notes |
|--------|---------|-------|
| `handlePassthrough` | Forwards arbitrary requests to VM | We handle all methods directly |
| `handlePersistentRPC` | Long-lived bidirectional RPC | May be used for future streaming features |
| `SetCondaDiskPath` | Conda environment management | Native Linux uses host conda directly |

**Newly handled in v1.1.9669:** `handleCreateDiskImage`, `getSessionsDiskInfo`, `deleteSessionDirs` (all no-ops on native Linux).

**v1.2.234:** No new handler functions. Binary is a rebuild with updated timestamps only (identical size).

**v1.569.0:** New handler `handleSendGuestResponse` for plugin permission bridge guest responses. Binary grew ~11KB.

---

## bin/ Directory Checksums (v1.569.0)

| File | SHA256 |
|------|--------|
| cowork-svc.exe | a31c0aed813d1a8384c31e6fdbcc0fc4b48c9c4d5ebefe659dea274a06101387 |
| cowork-plugin-shim.sh | 2fbef5ee6c07c26a1f7cd9204e1b6d37537edd2b96c0ce025010b890cb5935e7 |
| chrome-native-host.exe | *(check with sha256sum)* |
| smol-bin.x64.vhdx | *(check with sha256sum)* |
| default.clod | *(check with sha256sum)* |

---

## app.asar Analysis (from bin/)

| Property | Value |
|----------|-------|
| **Package** | @ant/desktop v1.569.0 |
| **Electron** | 40.8.5 |
| **Node requirement** | >=22.0.0 |

### New in v1.1.9669

- **coworkArtifact.js** — Electron preload script exposing `window.cowork.callMcpTool(toolName, params)` bridge for web artifacts to invoke MCP tools
- **Plugin/marketplace system** — Full plugin install/uninstall/sync via Electron IPC (`CustomPlugins` interface), not cowork-svc RPC
- **Conda integration** — `createDiskImage` RPC, `mountConda` spawn param, `manage_environments`/`manage_packages` tools
- **Scheduled tasks** — `coworkScheduledTasksEnabled` / `ccdScheduledTasksEnabled` settings (both default `false`)
- **New cowork tools**: `request_network_access`, `request_host_access`, `render_dashboard`/`patch_dashboard`/`read_dashboard`, `display_artifacts`
- **`--cowork` flag** — appended to CLI commands when `useCoworkFlag` is true

### New in v1.2.234

- **`dispatchCodeTasksPermissionMode`** — New preference controlling permission mode for dispatch code tasks: `"default"`, `"acceptEdits"`, `"plan"`, `"auto"`, `"bypassPermissions"` (default: `"acceptEdits"`)
- **`start_code_task` MCP tool** — New dispatch tool for code-specific tasks (in addition to `start_task`). Desktop prefers this for code work (editing repos, running tests)
- **Plugin permission bridge mounts** — Desktop now passes `.cowork-perm-req` (rw) and `.cowork-perm-resp` (ro) in `additionalMounts` for plugin confirmation protocol
- **`.cowork-lib` shim mount** — Plugin shim library mounted read-only at `mnt/.cowork-lib/shim.sh` for plugins to source
- **`remotePluginsPath`** — New internal Desktop parameter used to construct `additionalMounts` (not passed directly via RPC)
- **Electron 40.8.5** — Upgraded from 40.4.1
- **claude-agent-sdk-future 0.2.90-dev** — Updated from 0.2.86-dev

### New in v1.569.0

- **`sendGuestResponse` RPC method** — New handler in cowork-svc.exe for delivering host responses to VM guest processes (plugin permission bridge)
- **`navigateHost` IPC** — New CoworkArtifactBridge method for host navigation from artifacts
- **OperonSkills IPC** — Full CRUD for skills management (create, createFromFile, delete, get, list, listForAgent, update, attachAgents, detachAgent)
- **Local skills management** — New `saveLocalSkill`, `deleteLocalSkill`, `revealLocalSkill` handlers
- **SideChat** — New side-chat spawning functionality
- **Dispatch improvements** — `translateDispatchAttachments`, `startDispatchChildSession`, `detachDispatchChildren`
- **Teaching mode** — `cu_teach_session` telemetry events
- **Artifact lifecycle** — New telemetry events: `cowork_artifacts_created`, `cowork_artifacts_updated`, `cowork_artifacts_imported`, `cowork_artifacts_exported`
- **IPC UUID change** — Internal Electron IPC bridge UUID changed (no protocol impact)
- **SDK versions unchanged** — Same Electron 40.8.5, same claude-agent-sdk versions

### Key Dependency Versions

*(verified for v1.569.0 — unchanged from v1.2.234)*

| Package | Version | Changed from v1.1.9669 |
|---------|---------|------------------------|
| @anthropic-ai/claude-agent-sdk | 0.2.87 | — |
| @anthropic-ai/claude-agent-sdk-future | 0.2.90-dev.20260331 | was 0.2.86-dev.20260327 |
| @anthropic-ai/conway-client | 0.2.0-dev.20260325 | — |
| @anthropic-ai/mcpb | 2.1.2 | — |
| @anthropic-ai/sdk | ^0.70.0 | — |
| @modelcontextprotocol/sdk | 1.28.0 | — |
| electron | 40.8.5 | was 40.4.1 |
| playwright-core | 1.57.0 | — |
| typescript | ~5.8.3 | — |
| zod | ^3.25.64 | — |
| ws | ^8.18.0 | — |
| ssh2 | ^1.16.0 | — |

### Internal Workspace Packages

@ant/chrome-native-host, @ant/claude-ssh, @ant/cowork-win32-service, @ant/claude-screen-app, @ant/claude-swift-ant, @ant/computer-use-mcp, @ant/imagine-server, @anthropic-ai/operon-core, @anthropic-ai/operon-web

---

## Key Reverse Engineering Findings

1. The Go binary uses standard library HTTP/JSON, making protocol analysis straightforward
2. The vsock port 0xC822 (51234) is hardcoded in both platforms
3. The named pipe on Windows uses the same length-prefixed JSON protocol as Unix sockets
4. cowork-svc.exe includes a bundle downloader that fetches VM images from the CDN on first use
5. The smol-bin.vhdx is used as a side-loaded disk for updating sdk-daemon inside the VM
6. Spawn parameters match exactly between Windows and macOS (same field names, same JSON structure)

## What to Check on Update

1. Run `strings bin/cowork-svc.exe | grep -i "method\|spawn\|subscribe\|event"` for new RPC methods
2. Check if new files appear at the same directory level
3. Compare binary size — significant changes may indicate new functionality
4. Check the app.asar for changes to the TypeScript VM client (session management code)
5. Compare cowork-svc.exe SHA256 against previous version
6. Check Go version: `strings bin/cowork-svc.exe | grep "^go[0-9]"`
7. Check for new `handle` functions: `strings bin/cowork-svc.exe | grep "handle[A-Z]"`
8. Check app.asar dependency versions (especially @anthropic-ai/* and @modelcontextprotocol/sdk)
9. Look for new internal workspace packages

## Version History

| Claude Desktop Version | cowork-svc.exe Size | Notable Changes |
|----------------------|-------------------|-----------------|
| 1.569.0 | 11,186,000 bytes | New RPC method `sendGuestResponse` (plugin permission bridge); binary grew ~11KB |
| 1.2.234 | 11,174,736 bytes | Rebuild only; Electron 40.8.5, dispatchCodeTasksPermissionMode, plugin permission bridge mounts |
| 1.1.9669 | 11,174,736 bytes | New: cowork-plugin-shim.sh, conda disk support, plugin system, coworkArtifact.js |
| 1.1.9493 | 11,162,448 bytes | Previous |
| 1.1.9310 | (check previous) | — |
| 1.1.7464 | (original extraction) | First reverse engineering |
| 1.1.4173 | (initial discovery) | Original README reference |
