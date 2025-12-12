package mcp

type Metadata struct {
	Name        string `json:"name"`
	Version     string `json:"version,omitempty"`
	Description string `json:"description,omitempty"`
}

type Options struct {
	Server Metadata `json:"server"`
	Tool   Metadata `json:"tool"`
}
