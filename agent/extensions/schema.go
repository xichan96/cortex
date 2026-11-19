package extensions

// SchemaProvider is an optional interface for extension tools that expose a
// JSON Schema for their arguments. The core Tool interface deliberately stays
// minimal (raw JSON in, value out); hosts that adapt extension tools to a
// model-facing registry consult this interface to build the tool declaration.
//
// A tool that does not implement SchemaProvider is still callable; the host
// should fall back to a permissive object schema.
type SchemaProvider interface {
	Schema() map[string]any
}
