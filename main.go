package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
)

// Peer represents a connected peer
type Peer struct {
	Conn    net.Conn
	Address string
}

// Node represents our local P2P node
type Node struct {
	Address   string
	Peers     map[string]*Peer
	Transfers map[string]*FileTransfer // keyed by file hash
	mu        sync.RWMutex
	listener  net.Listener

	// For outgoing transfers: store chunks to send
	OutgoingChunks map[string][][]byte // keyed by file hash
}

// NewNode creates a new P2P node that listens on the given port
func NewNode(port string) (*Node, error) {
	ln, err := net.Listen("tcp", ":"+port)
	if err != nil {
		return nil, fmt.Errorf("failed to start listener: %w", err)
	}

	node := &Node{
		Address:        "localhost:" + port,
		Peers:          make(map[string]*Peer),
		Transfers:      make(map[string]*FileTransfer),
		OutgoingChunks: make(map[string][][]byte),
		listener:       ln,
	}

	fmt.Printf("Node listening on port %s\n", port)
	return node, nil
}

// Start begins accepting incoming connections
func (n *Node) Start() {
	go n.acceptConnections()
}

func (n *Node) acceptConnections() {
	for {
		conn, err := n.listener.Accept()
		if err != nil {
			fmt.Printf("Error accepting connection: %v\n", err)
			continue
		}

		addr := conn.RemoteAddr().String()
		fmt.Printf("\nNew peer connected: %s\n> ", addr)

		n.mu.Lock()
		n.Peers[addr] = &Peer{Conn: conn, Address: addr}
		n.mu.Unlock()

		go n.handlePeer(addr)
	}
}

// ConnectToPeer connects to another peer
func (n *Node) ConnectToPeer(address string) error {
	conn, err := net.Dial("tcp", address)
	if err != nil {
		return fmt.Errorf("failed to connect to %s: %w", address, err)
	}

	fmt.Printf("Connected to peer: %s\n", address)

	n.mu.Lock()
	n.Peers[address] = &Peer{Conn: conn, Address: address}
	n.mu.Unlock()

	go n.handlePeer(address)
	return nil
}

// handlePeer processes incoming messages from a peer
func (n *Node) handlePeer(address string) {
	n.mu.RLock()
	peer, exists := n.Peers[address]
	n.mu.RUnlock()

	if !exists {
		return
	}

	scanner := bufio.NewScanner(peer.Conn)
	// Increase buffer size for chunk transfers
	scanner.Buffer(make([]byte, 0), 1024*1024)

	for scanner.Scan() {
		var msg Message
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			fmt.Printf("\nBad message from %s: %v\n> ", address, err)
			continue
		}

		switch msg.Type {
		case MsgText:
			var text string
			json.Unmarshal(msg.Payload, &text)
			fmt.Printf("\n[%s]: %s\n> ", address, text)

		case MsgFileOffer:
			n.handleFileOffer(address, msg.Payload)

		case MsgFileAccept:
			n.handleFileAccept(address, msg.Payload)

		case MsgChunk:
			n.handleChunk(address, msg.Payload)

		case MsgChunkAck:
			// Could add retry logic here later

		case MsgComplete:
			var hash string
			json.Unmarshal(msg.Payload, &hash)
			fmt.Printf("\n[%s] confirmed file transfer complete: %s\n> ", address, hash[:16])
		}
	}

	fmt.Printf("\nPeer disconnected: %s\n> ", address)
	n.mu.Lock()
	delete(n.Peers, address)
	n.mu.Unlock()
}

// handleFileOffer processes an incoming file offer
func (n *Node) handleFileOffer(from string, payload json.RawMessage) {
	var offer FileOffer
	json.Unmarshal(payload, &offer)

	fmt.Printf("\n[%s] is offering file: %s (%d bytes, %d chunks)\n",
		from, offer.FileName, offer.FileSize, offer.TotalChunks)

	// Auto-accept for now (could prompt user later)
	ft := NewFileTransfer(offer)
	n.mu.Lock()
	n.Transfers[offer.FileHash] = ft
	n.mu.Unlock()

	fmt.Printf("Accepting file transfer...\n> ")

	// Send accept
	data, _ := NewMessage(MsgFileAccept, offer.FileHash)
	n.sendRaw(from, data)
}

// handleFileAccept processes a file accept and starts sending chunks
func (n *Node) handleFileAccept(from string, payload json.RawMessage) {
	var hash string
	json.Unmarshal(payload, &hash)

	n.mu.RLock()
	chunks, exists := n.OutgoingChunks[hash]
	n.mu.RUnlock()

	if !exists {
		fmt.Printf("\nNo outgoing transfer found for hash %s\n> ", hash[:16])
		return
	}

	fmt.Printf("\nPeer accepted! Sending %d chunks...\n> ", len(chunks))

	// Send chunks in a goroutine
	go func() {
		for i, chunk := range chunks {
			cd := ChunkData{
				Index: i,
				Data:  chunk,
			}
			data, err := NewMessage(MsgChunk, cd)
			if err != nil {
				fmt.Printf("\nError encoding chunk %d: %v\n> ", i, err)
				return
			}
			n.sendRaw(from, data)

			pct := float64(i+1) / float64(len(chunks)) * 100
			fmt.Printf("\rSending: %.0f%% (%d/%d chunks)", pct, i+1, len(chunks))
		}
		fmt.Printf("\nAll chunks sent!\n> ")
	}()
}

// handleChunk processes a received chunk
func (n *Node) handleChunk(from string, payload json.RawMessage) {
	var chunk ChunkData
	json.Unmarshal(payload, &chunk)

	// Find the matching transfer
	n.mu.RLock()
	var ft *FileTransfer
	for _, t := range n.Transfers {
		if chunk.Index < t.Offer.TotalChunks {
			ft = t
			break
		}
	}
	n.mu.RUnlock()

	if ft == nil {
		return
	}

	ok := ft.AddChunk(chunk)

	// Send ack
	ack := ChunkAck{Index: chunk.Index, OK: ok}
	data, _ := NewMessage(MsgChunkAck, ack)
	n.sendRaw(from, data)

	pct := float64(ft.ReceivedCount) / float64(ft.Offer.TotalChunks) * 100
	fmt.Printf("\rReceiving: %.0f%% (%d/%d chunks)", pct, ft.ReceivedCount, ft.Offer.TotalChunks)

	// Check if complete
	if ft.IsComplete() {
		fmt.Println()
		if err := ft.Assemble(); err != nil {
			fmt.Printf("Error assembling file: %v\n> ", err)
			return
		}

		// Notify sender
		data, _ := NewMessage(MsgComplete, ft.Offer.FileHash)
		n.sendRaw(from, data)
		fmt.Print("> ")
	}
}

// SendFile offers a file to a peer
func (n *Node) SendFile(address string, filePath string) error {
	offer, chunks, err := ChunkFile(filePath)
	if err != nil {
		return err
	}

	// Store chunks for when peer accepts
	n.mu.Lock()
	n.OutgoingChunks[offer.FileHash] = chunks
	n.mu.Unlock()

	fmt.Printf("Offering file: %s (%d bytes, %d chunks, hash: %s)\n",
		offer.FileName, offer.FileSize, offer.TotalChunks, offer.FileHash[:16])

	data, err := NewMessage(MsgFileOffer, offer)
	if err != nil {
		return err
	}

	return n.sendRaw(address, data)
}

// sendRaw sends raw bytes to a peer
func (n *Node) sendRaw(address string, data []byte) error {
	n.mu.RLock()
	peer, exists := n.Peers[address]
	n.mu.RUnlock()

	if !exists {
		return fmt.Errorf("peer %s not found", address)
	}

	_, err := peer.Conn.Write(data)
	return err
}

// SendTextMessage sends a text message to a peer
func (n *Node) SendTextMessage(address string, text string) error {
	data, err := NewMessage(MsgText, text)
	if err != nil {
		return err
	}
	return n.sendRaw(address, data)
}

// Broadcast sends a text message to all peers
func (n *Node) Broadcast(message string) {
	data, _ := NewMessage(MsgText, message)
	n.mu.RLock()
	defer n.mu.RUnlock()

	for addr, peer := range n.Peers {
		_, err := peer.Conn.Write(data)
		if err != nil {
			fmt.Printf("Error sending to %s: %v\n", addr, err)
		}
	}
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run . <port>")
		fmt.Println("Example: go run . 8000")
		os.Exit(1)
	}

	port := os.Args[1]

	node, err := NewNode(port)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}

	node.Start()

	reader := bufio.NewReader(os.Stdin)
	fmt.Println("\nCommands:")
	fmt.Println("  connect <host:port>         - Connect to a peer")
	fmt.Println("  send <host:port> <msg>      - Send text message")
	fmt.Println("  sendfile <host:port> <path> - Send a file")
	fmt.Println("  broadcast <msg>             - Message all peers")
	fmt.Println("  peers                       - List connected peers")
	fmt.Println("  quit                        - Exit")
	fmt.Println()

	for {
		fmt.Print("> ")
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(input)

		parts := strings.SplitN(input, " ", 3)
		if len(parts) == 0 || parts[0] == "" {
			continue
		}

		switch parts[0] {
		case "connect":
			if len(parts) < 2 {
				fmt.Println("Usage: connect <host:port>")
				continue
			}
			if err := node.ConnectToPeer(parts[1]); err != nil {
				fmt.Printf("Error: %v\n", err)
			}

		case "send":
			if len(parts) < 3 {
				fmt.Println("Usage: send <host:port> <message>")
				continue
			}
			if err := node.SendTextMessage(parts[1], parts[2]); err != nil {
				fmt.Printf("Error: %v\n", err)
			}

		case "sendfile":
			if len(parts) < 3 {
				fmt.Println("Usage: sendfile <host:port> <filepath>")
				continue
			}
			if err := node.SendFile(parts[1], parts[2]); err != nil {
				fmt.Printf("Error: %v\n", err)
			}

		case "broadcast":
			if len(parts) < 2 {
				fmt.Println("Usage: broadcast <message>")
				continue
			}
			node.Broadcast(strings.Join(parts[1:], " "))

		case "peers":
			node.mu.RLock()
			if len(node.Peers) == 0 {
				fmt.Println("No connected peers")
			} else {
				fmt.Println("Connected peers:")
				for addr := range node.Peers {
					fmt.Printf("  - %s\n", addr)
				}
			}
			node.mu.RUnlock()

		case "quit":
			fmt.Println("Shutting down...")
			os.Exit(0)

		default:
			fmt.Println("Unknown command")
		}
	}
}
