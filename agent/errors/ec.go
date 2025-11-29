package errors

// Error code constant definitions
var (
	// Generic errors (1xxx)
	EC_AGENT_BUSY              = NewAgentError(1001, "agent is already running")                  // 1001
	EC_CHAT_FAILED             = NewAgentError(1002, "failed to chat with tools")                 // 1002
	EC_STREAM_CHAT_FAILED      = NewAgentError(1003, "failed to chat with tools in stream")       // 1003
	EC_STREAM_ERROR            = NewAgentError(1004, "stream error occurred")                     // 1004
	EC_STREAM_ITERATION_FAILED = NewAgentError(1005, "stream iteration failed")                   // 1005
	EC_STREAM_PANIC            = NewAgentError(1006, "panic in stream execution")                 // 1006
	EC_PREPARE_MESSAGES_FAILED = NewAgentError(1007, "failed to prepare messages")                // 1007
	EC_ITERATION_FAILED        = NewAgentError(1008, "iteration failed")                          // 1008
	EC_BLOCKING_CHAT_FAILED    = NewAgentError(1009, "failed to get tool calls in blocking mode") // 1009

	// Tool-related errors (2xxx)
	EC_TOOL_EXECUTION_FAILED  = NewAgentError(2001, "tool execution failed")  // 2001
	EC_TOOL_NOT_FOUND         = NewAgentError(2002, "tool not found")         // 2002
	EC_TOOL_VALIDATION_FAILED = NewAgentError(2003, "tool validation failed") // 2003
	EC_TOOL_PARAMETER_INVALID = NewAgentError(2004, "tool parameter invalid") // 2004
	EC_TOOL_EXECUTION_TIMEOUT = NewAgentError(2005, "tool execution timeout") // 2005

	// Configuration errors (3xxx)
	EC_INVALID_CONFIG           = NewAgentError(3001, "invalid configuration")           // 3001
	EC_MISSING_CONFIG           = NewAgentError(3002, "missing configuration")           // 3002
	EC_CONFIG_PARSE_FAILED      = NewAgentError(3003, "configuration parse failed")      // 3003
	EC_CONFIG_VALIDATION_FAILED = NewAgentError(3004, "configuration validation failed") // 3004

	// Memory/cache errors (4xxx)
	EC_MEMORY_ERROR             = NewAgentError(4001, "memory error")             // 4001
	EC_CACHE_ERROR              = NewAgentError(4002, "cache error")              // 4002
	EC_CACHE_FULL               = NewAgentError(4003, "cache full")               // 4003
	EC_MEMORY_ALLOCATION_FAILED = NewAgentError(4004, "memory allocation failed") // 4004

	// Network/connection errors (5xxx)
	EC_NETWORK_ERROR       = NewAgentError(5001, "network error")       // 5001
	EC_CONNECTION_FAILED   = NewAgentError(5002, "connection failed")   // 5002
	EC_TIMEOUT             = NewAgentError(5003, "operation timeout")   // 5003
	EC_CONNECTION_TIMEOUT  = NewAgentError(5004, "connection timeout")  // 5004
	EC_NETWORK_UNREACHABLE = NewAgentError(5005, "network unreachable") // 5005

	// Validation errors (6xxx)
	EC_VALIDATION_FAILED = NewAgentError(6001, "validation failed") // 6001
	EC_INVALID_INPUT     = NewAgentError(6002, "invalid input")     // 6002
	EC_INVALID_STATE     = NewAgentError(6003, "invalid state")     // 6003
	EC_PARAMETER_MISSING = NewAgentError(6004, "parameter missing") // 6004
	EC_PARAMETER_INVALID = NewAgentError(6005, "parameter invalid") // 6005

	// System errors (7xxx)
	EC_INTERNAL_ERROR     = NewAgentError(7001, "internal error")     // 7001
	EC_RESOURCE_EXHAUSTED = NewAgentError(7002, "resource exhausted") // 7002
	EC_NOT_IMPLEMENTED    = NewAgentError(7003, "not implemented")    // 7003
	EC_UNKNOWN_ERROR      = NewAgentError(7004, "unknown error")      // 7004
	EC_SYSTEM_OVERLOAD    = NewAgentError(7005, "system overload")    // 7005

	// Data errors (8xxx)
	EC_DATA_CORRUPTION     = NewAgentError(8001, "data corruption")     // 8001
	EC_DATA_NOT_FOUND      = NewAgentError(8002, "data not found")      // 8002
	EC_DATA_FORMAT_INVALID = NewAgentError(8003, "data format invalid") // 8003
	EC_DATA_SIZE_EXCEEDED  = NewAgentError(8004, "data size exceeded")  // 8004

	// Permission/authentication errors (9xxx)
	EC_UNAUTHORIZED          = NewAgentError(9001, "unauthorized")          // 9001
	EC_FORBIDDEN             = NewAgentError(9002, "forbidden")             // 9002
	EC_AUTHENTICATION_FAILED = NewAgentError(9003, "authentication failed") // 9003
	EC_PERMISSION_DENIED     = NewAgentError(9004, "permission denied")     // 9004
)
