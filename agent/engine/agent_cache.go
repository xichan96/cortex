package engine

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/xichan96/cortex/agent/types"
)

// ==================== Cache Management Methods ====================

// generateToolCacheKey generates a tool cache key
// Generates a unique cache key based on tool name and parameters
// Uses tool name prefix to reduce collision probability
// Parameters:
//   - toolName: tool name
//   - args: tool parameters
//
// Returns:
//   - cache key string
func generateToolCacheKey(toolName string, args map[string]any) string {
	hasher := md5.New()
	hasher.Write([]byte("tool:" + toolName + ":"))

	if len(args) > 0 {
		argsJSON, err := json.Marshal(args)
		if err != nil {
			hasher.Write([]byte("fallback:"))
			hasher.Write([]byte(fmt.Sprint(args)))
		} else {
			hasher.Write(argsJSON)
		}
	}

	return toolName + ":" + hex.EncodeToString(hasher.Sum(nil))
}

// getToolTruncationLength gets truncation length for a tool
// Returns tool-specific truncation length from metadata, or default if not set
// Parameters:
//   - toolName: tool name
//
// Returns:
//   - truncation length
func (ae *AgentEngine) getToolTruncationLength(toolName string) int {
	ae.mu.RLock()
	tool, exists := ae.toolsMap[toolName]
	ae.mu.RUnlock()

	if exists {
		metadata := tool.Metadata()
		if metadata.MaxTruncationLength > 0 {
			return metadata.MaxTruncationLength
		}
	}
	if c := ae.getConfig(); c != nil && c.DefaultToolResultMaxLen > 0 {
		return c.DefaultToolResultMaxLen
	}
	return types.MaxTruncationLength
}

func (ae *AgentEngine) getToolErrorMaxLen() int {
	if c := ae.getConfig(); c != nil && c.ToolErrorMaxLen > 0 {
		return c.ToolErrorMaxLen
	}
	return types.ToolErrorMaxLen
}

// getCachedToolResult gets cached tool result
// Retrieves tool execution result from cache to avoid repeated execution
// Updates LRU order on cache hit
// Parameters:
//   - toolName: tool name
//   - args: tool parameters
//
// Returns:
//   - tool execution result
//   - execution error (if any)
//   - whether cache was found
func (ae *AgentEngine) getCachedToolResult(toolName string, args map[string]any) (any, error, bool) {
	cacheKey := generateToolCacheKey(toolName, args)

	ae.toolCacheMu.Lock()
	defer ae.toolCacheMu.Unlock()

	entry, exists := ae.toolCache[cacheKey]
	if !exists {
		return nil, nil, false
	}

	// Check expiration
	if time.Since(entry.Timestamp) >= types.CacheExpirationTime {
		ae.removeCacheEntry(entry)
		return nil, nil, false
	}

	// Move to head (most recently used)
	ae.moveToHead(entry)
	return entry.Result, entry.Err, true
}

// setCachedToolResult sets tool result cache
// Caches tool execution result to avoid repeated execution of the same tool call
// Uses LRU eviction strategy: removes expired entries first, then least recently used entries
// Parameters:
//   - toolName: tool name
//   - args: tool parameters
//   - result: tool execution result
//   - err: execution error (if any)
func (ae *AgentEngine) setCachedToolResult(toolName string, args map[string]any, result any, err error) {
	cacheKey := generateToolCacheKey(toolName, args)

	ae.toolCacheMu.Lock()
	defer ae.toolCacheMu.Unlock()

	// Check if entry already exists (update existing entry)
	if existing, exists := ae.toolCache[cacheKey]; exists {
		existing.Result = result
		existing.Err = err
		existing.Timestamp = time.Now()
		ae.moveToHead(existing)
		return
	}

	// Remove expired entries first
	ae.removeExpiredEntries()

	// If cache is still full, remove least recently used entries (from tail)
	for len(ae.toolCache) >= ae.toolCacheSize && ae.toolCacheTail != nil {
		ae.removeCacheEntry(ae.toolCacheTail)
	}

	// Create new entry and add to head
	entry := &types.ToolCacheEntry{
		Result:    result,
		Err:       err,
		Timestamp: time.Now(),
		Key:       cacheKey,
	}
	ae.toolCache[cacheKey] = entry
	ae.addToHead(entry)
}

// removeExpiredEntries removes all expired cache entries
func (ae *AgentEngine) removeExpiredEntries() {
	now := time.Now()
	current := ae.toolCacheTail
	for current != nil {
		next := current.Prev
		if now.Sub(current.Timestamp) >= types.CacheExpirationTime {
			ae.removeCacheEntry(current)
		}
		current = next
	}
}

// addToHead adds an entry to the head of LRU list
func (ae *AgentEngine) addToHead(entry *types.ToolCacheEntry) {
	entry.Prev = nil
	entry.Next = ae.toolCacheHead

	if ae.toolCacheHead != nil {
		ae.toolCacheHead.Prev = entry
	} else {
		ae.toolCacheTail = entry
	}
	ae.toolCacheHead = entry
}

// moveToHead moves an existing entry to the head of LRU list
func (ae *AgentEngine) moveToHead(entry *types.ToolCacheEntry) {
	if entry == ae.toolCacheHead {
		return
	}

	// Remove from current position
	if entry.Prev != nil {
		entry.Prev.Next = entry.Next
	}
	if entry.Next != nil {
		entry.Next.Prev = entry.Prev
	} else {
		ae.toolCacheTail = entry.Prev
	}

	// Add to head
	entry.Prev = nil
	entry.Next = ae.toolCacheHead
	if ae.toolCacheHead != nil {
		ae.toolCacheHead.Prev = entry
	}
	ae.toolCacheHead = entry
}

// removeCacheEntry removes an entry from cache and LRU list
func (ae *AgentEngine) removeCacheEntry(entry *types.ToolCacheEntry) {
	if entry == nil {
		return
	}

	delete(ae.toolCache, entry.Key)

	if entry.Prev != nil {
		entry.Prev.Next = entry.Next
	} else {
		ae.toolCacheHead = entry.Next
	}

	if entry.Next != nil {
		entry.Next.Prev = entry.Prev
	} else {
		ae.toolCacheTail = entry.Prev
	}

	entry.Prev = nil
	entry.Next = nil
}
