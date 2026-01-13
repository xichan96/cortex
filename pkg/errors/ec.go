package errors

// Error code constant definitions
var (
	// Generic errors (1xxx)
	EC_AGENT_BUSY              = NewError(1001, "agent is already running")                  // 1001
	EC_CHAT_FAILED             = NewError(1002, "failed to chat with tools")                 // 1002
	EC_STREAM_CHAT_FAILED      = NewError(1003, "failed to chat with tools in stream")       // 1003
	EC_STREAM_ERROR            = NewError(1004, "stream error occurred")                     // 1004
	EC_STREAM_ITERATION_FAILED = NewError(1005, "stream iteration failed")                   // 1005
	EC_STREAM_PANIC            = NewError(1006, "panic in stream execution")                 // 1006
	EC_PREPARE_MESSAGES_FAILED = NewError(1007, "failed to prepare messages")                // 1007
	EC_ITERATION_FAILED        = NewError(1008, "iteration failed")                          // 1008
	EC_BLOCKING_CHAT_FAILED    = NewError(1009, "failed to get tool calls in blocking mode") // 1009
	EC_MEMORY_HISTORY_FAILED   = NewError(1010, "failed to get chat history")                // 1010

	// Tool-related errors (2xxx)
	EC_TOOL_EXECUTION_FAILED   = NewError(2001, "tool execution failed")   // 2001
	EC_TOOL_NOT_FOUND          = NewError(2002, "tool not found")          // 2002
	EC_TOOL_VALIDATION_FAILED  = NewError(2003, "tool validation failed")  // 2003
	EC_TOOL_PARAMETER_INVALID  = NewError(2004, "tool parameter invalid")  // 2004
	EC_TOOL_EXECUTION_TIMEOUT  = NewError(2005, "tool execution timeout")  // 2005
	EC_TOOL_ALREADY_REGISTERED = NewError(2006, "tool already registered") // 2006

	// Configuration errors (3xxx)
	EC_INVALID_CONFIG           = NewError(3001, "invalid configuration")           // 3001
	EC_MISSING_CONFIG           = NewError(3002, "missing configuration")           // 3002
	EC_CONFIG_PARSE_FAILED      = NewError(3003, "configuration parse failed")      // 3003
	EC_CONFIG_VALIDATION_FAILED = NewError(3004, "configuration validation failed") // 3004

	// Memory/cache errors (4xxx)
	EC_MEMORY_ERROR             = NewError(4001, "memory error")             // 4001
	EC_CACHE_ERROR              = NewError(4002, "cache error")              // 4002
	EC_CACHE_FULL               = NewError(4003, "cache full")               // 4003
	EC_MEMORY_ALLOCATION_FAILED = NewError(4004, "memory allocation failed") // 4004

	// Network/connection errors (5xxx)
	EC_NETWORK_ERROR       = NewError(5001, "network error")       // 5001
	EC_CONNECTION_FAILED   = NewError(5002, "connection failed")   // 5002
	EC_TIMEOUT             = NewError(5003, "operation timeout")   // 5003
	EC_CONNECTION_TIMEOUT  = NewError(5004, "connection timeout")  // 5004
	EC_NETWORK_UNREACHABLE = NewError(5005, "network unreachable") // 5005

	// Validation errors (6xxx)
	EC_VALIDATION_FAILED = NewError(6001, "validation failed") // 6001
	EC_INVALID_INPUT     = NewError(6002, "invalid input")     // 6002
	EC_INVALID_STATE     = NewError(6003, "invalid state")     // 6003
	EC_PARAMETER_MISSING = NewError(6004, "parameter missing") // 6004
	EC_PARAMETER_INVALID = NewError(6005, "parameter invalid") // 6005

	// System errors (7xxx)
	EC_INTERNAL_ERROR     = NewError(7001, "internal error")     // 7001
	EC_RESOURCE_EXHAUSTED = NewError(7002, "resource exhausted") // 7002
	EC_NOT_IMPLEMENTED    = NewError(7003, "not implemented")    // 7003
	EC_UNKNOWN_ERROR      = NewError(7004, "unknown error")      // 7004
	EC_SYSTEM_OVERLOAD    = NewError(7005, "system overload")    // 7005
	ErrRateLimitExceeded  = NewError(7006, "rate limit exceeded")

	// Data errors (8xxx)
	EC_DATA_CORRUPTION     = NewError(8001, "data corruption")     // 8001
	EC_DATA_NOT_FOUND      = NewError(8002, "data not found")      // 8002
	EC_DATA_FORMAT_INVALID = NewError(8003, "data format invalid") // 8003
	EC_DATA_SIZE_EXCEEDED  = NewError(8004, "data size exceeded")  // 8004

	// Permission/authentication errors (9xxx)
	EC_UNAUTHORIZED          = NewError(9001, "unauthorized")          // 9001
	EC_FORBIDDEN             = NewError(9002, "forbidden")             // 9002
	EC_AUTHENTICATION_FAILED = NewError(9003, "authentication failed") // 9003
	EC_PERMISSION_DENIED     = NewError(9004, "permission denied")     // 9004

	// LLM provider errors (10xxx)
	EC_LLM_NO_RESPONSE          = NewError(10001, "no response content")         // 10001
	EC_LLM_CALL_FAILED          = NewError(10002, "LLM call failed")             // 10002
	EC_LLM_API_KEY_REQUIRED     = NewError(10003, "API key is required")         // 10003
	EC_LLM_CLIENT_CREATE_FAILED = NewError(10004, "failed to create LLM client") // 10004

	// MCP client errors (11xxx)
	EC_MCP_UNSUPPORTED_TRANSPORT = NewError(11001, "unsupported transport")           // 11001
	EC_MCP_CLIENT_CREATE_FAILED  = NewError(11002, "failed to create MCP client")     // 11002
	EC_MCP_CLIENT_START_FAILED   = NewError(11003, "failed to start MCP client")      // 11003
	EC_MCP_CLIENT_INIT_FAILED    = NewError(11004, "failed to initialize MCP client") // 11004
	EC_MCP_REFRESH_TOOLS_FAILED  = NewError(11005, "failed to refresh tools")         // 11005
	EC_MCP_NOT_CONNECTED         = NewError(11006, "not connected to MCP server")     // 11006
	EC_MCP_CALL_TOOL_FAILED      = NewError(11007, "failed to call tool")             // 11007
	EC_MCP_TOOL_RETURNED_ERROR   = NewError(11008, "tool returned error")             // 11008
	EC_MCP_NO_ACTIVE_CLIENT      = NewError(11009, "no active client")                // 11009
	EC_MCP_GET_TOOLS_FAILED      = NewError(11010, "failed to get tools from server") // 11010
	EC_MCP_TOOL_NOT_CONNECTED    = NewError(11011, "tool not connected")              // 11011

	// HTTP client errors (12xxx)
	EC_HTTP_REQUEST_FAILED = NewError(12001, "HTTP request failed")              // 12001
	EC_HTTP_MARSHAL_FAILED = NewError(12002, "failed to marshal request body")   // 12002
	EC_HTTP_STATUS_ERROR   = NewError(12003, "request failed with status error") // 12003

	// HTTP server errors (12xxx)
	EC_HTTP_INVALID_REQUEST       = NewError(12004, "invalid request parameters")               // 12004
	EC_HTTP_MESSAGE_EMPTY         = NewError(12005, "message parameter cannot be empty")        // 12005
	EC_HTTP_EXECUTE_FAILED        = NewError(12006, "failed to execute agent engine")           // 12006
	EC_HTTP_STREAM_EXECUTE_FAILED = NewError(12007, "failed to execute agent engine in stream") // 12007
	EC_HTTP_INVALID_METHOD        = NewError(12008, "invalid HTTP method")                      // 12008
	EC_HTTP_INVALID_SESSION_ID    = NewError(12009, "invalid session ID")                       // 12009
	EC_HTTP_SESSION_NOT_FOUND     = NewError(12010, "session not found")                        // 12010

	// Email errors (13xxx)
	EC_EMAIL_SEND_FAILED = NewError(13001, "failed to send email") // 13001

	// Cache errors (14xxx)
	EC_CACHE_NO_FOUND = NewError(14001, "cache not found") // 14001

	// SQL/database errors (15xxx)
	EC_SQL_NOT_FOUND      = NewError(15001, "record not found")        // 15001
	EC_SQL_DUPLICATE_KEY  = NewError(15002, "duplicate key error")    // 15002
	EC_SQL_DB_KEY_ERROR   = NewError(15003, "ctx dbkey type error")   // 15003
	EC_SQL_DEFAULT_DB_ERROR = NewError(15004, "empty default db")     // 15004
	EC_SQL_ERROR          = NewError(15005, "SQL error")              // 15005
)
