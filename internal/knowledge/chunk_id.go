// Package knowledge — deterministic content-addressable chunk ID generation.
//
// Chunk IDs are derived from the SHA256 hash of the chunk content (including
// overlap context). This gives three key properties:
//
//  1. Idempotency — re-processing the same document with the same chunking
//     strategy yields the same IDs; re-embedding and re-indexing can be
//     skipped for unchanged chunks.
//  2. Incremental diff — when a document is modified, comparing old and new
//     chunk ID lists identifies added / removed / unchanged chunks, enabling
//     incremental (rather than full-rebuild) updates.
//  3. Strategy-change detection — when the chunking strategy version changes,
//     all IDs change, which signals a full rebuild (versioned index swap).
//
// Format: 12 hex chars (48 bits) — collision-safe for document-scale use.
package knowledge

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

const (
	// ChunkIDHexLen is the number of hex characters in a chunk ID (48 bits).
	ChunkIDHexLen = 12

	// ChunkIDFormatLen is the total string length of a chunk ID ("C" prefix + hex).
	ChunkIDFormatLen = 1 + ChunkIDHexLen
)

// ComputeChunkID returns a deterministic content-based chunk ID.
// It uses SHA256(content) and returns the first ChunkIDHexLen hex chars
// prefixed with "C" (e.g. "C3f2a8b1c0d1").
//
// The prefix "C" distinguishes content-hash IDs from the legacy sequential
// IDs ("000", "001", …), making it safe to mix both in the same index
// during migration from sequential to content-addressed IDs.
func ComputeChunkID(content string) string {
	h := sha256.Sum256([]byte(content))
	return "C" + hex.EncodeToString(h[:])[:ChunkIDHexLen]
}

// ComputeChunkIDs returns deterministic chunk IDs for a slice of chunk contents.
// Equivalent to calling ComputeChunkID on each element.
func ComputeChunkIDs(contents []string) []string {
	ids := make([]string, len(contents))
	for i, c := range contents {
		ids[i] = ComputeChunkID(c)
	}
	return ids
}

// ParseLegacyChunkID converts a legacy sequential ID (e.g. "005") to its
// zero-based integer index. Returns -1 if the ID is not a valid sequential ID.
// Content-addressable IDs (starting with "C") always return -1.
func ParseLegacyChunkID(chunkID string) int {
	if len(chunkID) == 0 || chunkID[0] == 'C' {
		return -1
	}
	id := 0
	for _, r := range chunkID {
		if r >= '0' && r <= '9' {
			id = id*10 + int(r-'0')
		} else {
			return -1
		}
	}
	return id
}

// IsLegacyChunkID reports whether the chunk ID uses the legacy sequential format.
func IsLegacyChunkID(chunkID string) bool {
	return len(chunkID) > 0 && chunkID[0] != 'C'
}

// IsContentChunkID reports whether the chunk ID uses the content-addressable format.
func IsContentChunkID(chunkID string) bool {
	return len(chunkID) > 0 && chunkID[0] == 'C'
}

// ChunkIDShort returns a shorter display form of a chunk ID (first 8 chars).
func ChunkIDShort(chunkID string) string {
	if len(chunkID) <= 8 {
		return chunkID
	}
	return chunkID[:8]
}

// SourceHash computes the SHA256 hex digest of raw source bytes.
// Used to detect whether a document has changed since the last index build.
func SourceHash(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// ChunkingStrategyVersion returns a composed version string that encodes the
// current chunking algorithm parameters. When any parameter changes, the
// version string changes, which signals that all chunk IDs must be
// recomputed (a full re-index).
//
// The returned string is used as the ChunkStrategy field in DocumentMeta and
// ChunkManifest to detect strategy changes across restarts.
func ChunkingStrategyVersion() string {
	// Embed the key parameters that affect chunk boundaries.
	// When these constants change in source, the strategy version changes.
	return fmt.Sprintf("v2-short%d-long%d-frag%d-overlap%d-sem%.2f",
		chunkShortChunk, chunkLongChunk, chunkFragmentThreshold, chunkOverlapChars, chunkSemanticThreshold)
}
