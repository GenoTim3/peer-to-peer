package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

const (
	ChunkSize   = 256 * 1024 // 256KB per chunk
	DownloadDir = "./downloads"
)

// Message types for our protocol
const (
	MsgText       = "TEXT"
	MsgFileOffer  = "FILE_OFFER"
	MsgFileAccept = "FILE_ACCEPT"
	MsgChunk      = "CHUNK"
	MsgChunkAck   = "CHUNK_ACK"
	MsgComplete   = "COMPLETE"
)

// Message is the envelope for all peer communication
type Message struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// FileOffer is sent to offer a file to a peer
type FileOffer struct {
	FileName    string   `json:"file_name"`
	FileSize    int64    `json:"file_size"`
	TotalChunks int      `json:"total_chunks"`
	FileHash    string   `json:"file_hash"`
	ChunkHashes []string `json:"chunk_hashes"`
}

// ChunkData represents a single chunk being transferred
type ChunkData struct {
	Index int    `json:"index"`
	Hash  string `json:"hash"`
	Data  []byte `json:"data"`
}

// ChunkAck acknowledges receipt of a chunk
type ChunkAck struct {
	Index int  `json:"index"`
	OK    bool `json:"ok"`
}

// FileTransfer tracks an in-progress file transfer
type FileTransfer struct {
	Offer         FileOffer
	Chunks        map[int][]byte
	ReceivedCount int
	mu            sync.Mutex
}

// ChunkFile splits a file into chunks and computes hashes
func ChunkFile(path string) (*FileOffer, [][]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to stat file: %w", err)
	}

	var chunks [][]byte
	var hashes []string
	fileHash := sha256.New()

	for {
		buf := make([]byte, ChunkSize)
		n, err := f.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			chunks = append(chunks, chunk)

			// Hash the chunk
			h := sha256.Sum256(chunk)
			hashes = append(hashes, hex.EncodeToString(h[:]))

			// Update full file hash
			fileHash.Write(chunk)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("failed to read file: %w", err)
		}
	}

	offer := &FileOffer{
		FileName:    filepath.Base(path),
		FileSize:    info.Size(),
		TotalChunks: len(chunks),
		FileHash:    hex.EncodeToString(fileHash.Sum(nil)),
		ChunkHashes: hashes,
	}

	return offer, chunks, nil
}

// NewFileTransfer creates a tracker for an incoming file
func NewFileTransfer(offer FileOffer) *FileTransfer {
	return &FileTransfer{
		Offer:  offer,
		Chunks: make(map[int][]byte),
	}
}

// AddChunk stores a received chunk and verifies its hash
func (ft *FileTransfer) AddChunk(chunk ChunkData) bool {
	// Verify hash
	h := sha256.Sum256(chunk.Data)
	got := hex.EncodeToString(h[:])

	if chunk.Index >= len(ft.Offer.ChunkHashes) || got != ft.Offer.ChunkHashes[chunk.Index] {
		fmt.Printf("Chunk %d hash mismatch! Expected %s, got %s\n",
			chunk.Index, ft.Offer.ChunkHashes[chunk.Index], got)
		return false
	}

	ft.mu.Lock()
	defer ft.mu.Unlock()

	ft.Chunks[chunk.Index] = chunk.Data
	ft.ReceivedCount++
	return true
}

// IsComplete checks if all chunks have been received
func (ft *FileTransfer) IsComplete() bool {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	return ft.ReceivedCount == ft.Offer.TotalChunks
}

// Assemble writes all chunks to a file in the downloads directory
func (ft *FileTransfer) Assemble() error {
	if err := os.MkdirAll(DownloadDir, 0755); err != nil {
		return fmt.Errorf("failed to create download dir: %w", err)
	}

	outPath := filepath.Join(DownloadDir, ft.Offer.FileName)
	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("failed to create output file: %w", err)
	}
	defer f.Close()

	fileHash := sha256.New()

	for i := 0; i < ft.Offer.TotalChunks; i++ {
		chunk, exists := ft.Chunks[i]
		if !exists {
			return fmt.Errorf("missing chunk %d", i)
		}
		if _, err := f.Write(chunk); err != nil {
			return fmt.Errorf("failed to write chunk %d: %w", i, err)
		}
		fileHash.Write(chunk)
	}

	// Verify full file hash
	got := hex.EncodeToString(fileHash.Sum(nil))
	if got != ft.Offer.FileHash {
		return fmt.Errorf("file hash mismatch! Expected %s, got %s", ft.Offer.FileHash, got)
	}

	fmt.Printf("File saved to %s (%d bytes, hash verified)\n", outPath, ft.Offer.FileSize)
	return nil
}

// Helper to create a Message with a typed payload
func NewMessage(msgType string, payload interface{}) ([]byte, error) {
	p, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	msg := Message{Type: msgType, Payload: json.RawMessage(p)}
	data, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	// Append newline as delimiter
	return append(data, '\n'), nil
}
