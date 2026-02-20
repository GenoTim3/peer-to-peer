# peer-to-peer

A peer-to-peer file sharing system built in Go. Nodes can connect directly to each other over TCP to exchange messages and transfer files with chunk-based integrity verification.

## Features

- **Direct P2P connections** — no central server required, peers connect directly via TCP
- **File chunking** — files are split into 256KB chunks for transfer
- **SHA-256 integrity checks** — every chunk and the final assembled file are hash-verified to ensure nothing is corrupted
- **Real-time text messaging** — send messages to individual peers or broadcast to all
- **Concurrent transfers** — built on Go's goroutines for non-blocking I/O
- **JSON protocol** — all communication uses a structured message format for extensibility

## How It Works

1. Each node listens on a TCP port and can accept incoming connections from other peers
2. Peers connect to each other using a simple `connect` command
3. When a file is sent, it is split into 256KB chunks. Each chunk is individually hashed with SHA-256
4. The sender transmits a file offer containing metadata (name, size, chunk count, and hashes) to the receiving peer
5. The receiver accepts the offer, and the sender streams all chunks over the connection
6. As each chunk arrives, its hash is verified against the expected value
7. Once all chunks are received, the file is reassembled in a `./downloads` directory and the full file hash is verified

## Prerequisites

- [Go 1.22+](https://go.dev/dl/)

## Getting Started

**Clone the repo:**

```bash
git clone <your-repo-url>
cd peer-to-peer
```

**Start a node:**

```bash
go run . <port>
```

**Example — two peers sharing a file:**

Terminal 1:
```bash
go run . 8000
```

Terminal 2:
```bash
go run . 8001
> connect localhost:8000
> sendfile localhost:8000 ./test.txt
```

The file will be transferred, verified, and saved to `./downloads/test.txt` on the receiving peer.

## Commands

| Command | Description |
|---|---|
| `connect <host:port>` | Connect to a peer |
| `send <host:port> <msg>` | Send a text message to a peer |
| `sendfile <host:port> <path>` | Send a file to a peer |
| `broadcast <msg>` | Send a message to all connected peers |
| `peers` | List all connected peers |
| `quit` | Shut down the node |

## Project Structure

```
peer-to-peer/
├── main.go        # Node logic, CLI, peer management, message handling
├── transfer.go    # File chunking, hashing, reassembly, and protocol types
├── downloads/     # Where received files are saved (created automatically)
├── go.mod
└── README.md
```

## Future Improvements

- Multi-peer downloads (fetch different chunks from different peers simultaneously)
- Peer discovery via a tracker or distributed hash table (DHT)
- Encrypted transfers using TLS
- Transfer progress bars and a terminal UI
- Resume interrupted transfers
- Peer reputation and bandwidth throttling