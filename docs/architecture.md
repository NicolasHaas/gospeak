# GoSpeak Architecture

GoSpeak is a privacy-focused voice communication server and client built in Go. It follows a Selective Forwarding Unit (SFU) architecture — the server relays encrypted voice packets between clients without decoding them.

## High-Level Overview

```mermaid
graph TB
    subgraph Clients
        C1[Client A<br/>Fyne GUI]
        C2[Client B<br/>Fyne GUI]
        C3[Client C<br/>Fyne GUI]
    end

    subgraph Server
        CTRL[Control Plane<br/>TCP/TLS 1.3<br/>:9600]
        SFU[Voice Plane<br/>UDP SFU<br/>:9601]
        DB[(SQLite<br/>Users, Channels,<br/>Tokens, Bans)]
    end

    C1 <-->|JSON over TLS| CTRL
    C2 <-->|JSON over TLS| CTRL
    C3 <-->|JSON over TLS| CTRL

    C1 <-->|AES-128-GCM<br/>Opus packets| SFU
    C2 <-->|AES-128-GCM<br/>Opus packets| SFU
    C3 <-->|AES-128-GCM<br/>Opus packets| SFU

    CTRL --- DB
```

## Package Structure

```mermaid
graph LR
    subgraph cmd
        CS[cmd/server] --> SRV
        CC[cmd/client] --> UI
    end

    subgraph pkg
        SRV[pkg/server]
        CLT[pkg/client]
        PROTO[pkg/protocol]
        PB[pkg/protocol/pb]
        AUDIO[pkg/audio]
        CRYPTO[pkg/crypto]
        MODEL[pkg/model]
        RBAC[pkg/rbac]
        STORE[pkg/store]
    end

    UI[ui/app.go]

    SRV --> PROTO
    SRV --> PB
    SRV --> CRYPTO
    SRV --> MODEL
    SRV --> RBAC
    SRV --> STORE

    CLT --> PROTO
    CLT --> PB
    CLT --> AUDIO
    CLT --> CRYPTO

    UI --> CLT
    UI --> AUDIO
    UI --> PB

    PROTO --> PB
    RBAC --> MODEL
    STORE --> MODEL
```

### Package Responsibilities

| Package | Description |
|---------|-------------|
| `cmd/server` | Server CLI entry point with flag parsing |
| `cmd/client` | Client entry point — launches the Fyne GUI |
| `pkg/server` | Server core: TLS listener, control handler, voice SFU, channel/session management, YAML config |
| `pkg/client` | Client engine: connection management, voice pipeline, jitter buffer, bookmarks, settings, hotkeys |
| `pkg/protocol` | Length-prefixed JSON framing for the control plane |
| `pkg/protocol/pb` | All control message type definitions (structs with JSON tags) |
| `pkg/audio` | PortAudio capture/playback, Opus encode/decode, VAD (Voice Activity Detection) |
| `pkg/crypto` | AES-128-GCM voice encryption, key generation, token hashing (SHA-256), password hashing (Argon2id) |
| `pkg/model` | Core domain types: User, Channel, Token, Ban, Session, Role, Permission |
| `pkg/rbac` | Role-based access control — permission matrix for User/Moderator/Admin |
| `pkg/store` | SQLite persistence with auto-migration |
| `ui` | Fyne v2 desktop GUI with channel tree, chat, settings, admin tools |

## Server Lifecycle

```mermaid
sequenceDiagram
    participant Main as cmd/server
    participant Srv as Server
    participant Store as SQLite
    participant TLS as TLS Listener
    participant UDP as UDP Listener

    Main->>Srv: New(config)
    Main->>Srv: Run()
    Srv->>Store: Open database
    Srv->>Store: Auto-migrate schema
    Srv->>Srv: GenerateKey() → shared AES-128 voice key
    Srv->>Store: Ensure "Lobby" channel exists
    Srv->>Store: Load channels from YAML (if configured)
    Srv->>Store: Ensure admin token exists (first run only)
    Srv->>TLS: StartControl(:9600)
    Srv->>UDP: StartVoice(:9601)
    Note over Srv: Server running — accepting connections
    Srv-->>Main: Block until SIGINT/SIGTERM
    Srv->>TLS: Close
    Srv->>UDP: Close
    Srv->>Store: Close
```

## Client Connection Flow

```mermaid
sequenceDiagram
    participant UI as Fyne GUI
    participant Eng as Engine
    participant TLS as TLS Connection
    participant UDP as UDP Voice
    participant Srv as Server

    UI->>Eng: Connect(host, token, username)
    Eng->>TLS: Dial TCP/TLS (skip verify for self-signed)
    TLS->>Srv: TLS 1.3 Handshake
    Eng->>Srv: AuthRequest{token, username}
    Srv->>Eng: AuthResponse{sessionID, role, encryptionKey, channels}
    Eng->>Eng: Create VoiceCipher from encryptionKey
    Eng->>UDP: Dial UDP to server:9601
    Eng->>Eng: Start audio capture + playback
    Eng->>UI: OnStateChange(Connected)
    Eng->>UI: OnChannelsUpdate(channels)

    loop Voice Loop
        Eng->>Eng: Capture PCM → VAD check → Opus encode
        Eng->>Srv: AES-128-GCM encrypted UDP packet
        Srv->>Eng: Relayed packets from others
        Eng->>Eng: Decrypt → Jitter buffer → Opus decode → Playback
    end
```

## Data Models

```mermaid
erDiagram
    USER {
        int64 id PK
        string username
        int role
        datetime created_at
    }
    CHANNEL {
        int64 id PK
        string name
        string description
        int max_users
        int64 parent_id FK
        bool is_temp
        bool allow_sub_channels
        datetime created_at
    }
    TOKEN {
        int64 id PK
        string hash
        int role
        int64 channel_scope FK
        int64 created_by FK
        int max_uses
        int use_count
        datetime expires_at
        datetime created_at
    }
    BAN {
        int64 id PK
        int64 user_id FK
        string ip
        string reason
        int64 banned_by FK
        datetime expires_at
        datetime created_at
    }

    USER ||--o{ TOKEN : "creates"
    USER ||--o{ BAN : "banned"
    CHANNEL ||--o{ CHANNEL : "parent"
```
