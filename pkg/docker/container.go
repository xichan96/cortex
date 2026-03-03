package docker

type Container struct {
	Name        string // only allow letters, numbers, and hyphens
	PullImage   bool
	ImageAuth   *ImageAuth
	Image       string
	Command     []string
	EntryPoint  []string
	Labels      map[string]string
	CPU         float32  // cpu cores
	Memory      float32  // memory in GB
	ShmSize     float32  // shm size in GB
	Ports       []string // ports
	Environment []string // environment variables
	Volumes     []Volume
	Links       []string
	NetworkName string
	HostNetwork bool
	HostIpcMode bool
	Privileged  bool
	Runtime     string
	IsRemove    bool
	Restart     string
	WorkDir     string
	Hosts       []string
	User        string // user to run the container, e.g. "root"
	// DisableCtrain bool
}

const (
	HostVolume = "host"
	NFSVolume  = "nfs"
	BindVolume = "bind"
)

type Volume struct {
	Type       string
	Server     string
	Source     string
	Target     string
	ReadOnly   bool
	Shared     bool
	Privileged bool
}

type ContainerLog struct {
	ContainerID string
	Lines       int
	Follow      bool
	Timestamp   bool
}

type ContainerTerminal struct {
	ContainerID string   `form:"container_id"`
	Cols        int      `form:"cols"`
	Rows        int      `form:"rows"`
	Env         []string `form:"env"`
	Cmd         string   `form:"cmd"`
	Input       chan []byte
}

type ImageAuth struct {
	Username string
	Password string
}
