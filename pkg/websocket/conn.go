package websocket

// Server ws cli manage
type Server struct {
	Register    chan *Client     // register connection handler
	Unregister  chan *Client     // unregister connection handler
	Heartbeat   chan *Client     // heartbeat handler
	ConnManager ConnManagerIface // connection manager
}

// NewServer initializes a new server.
func NewServer(connManage ConnManagerIface) (clientManager *Server) {
	go connManage.DestroyTask()
	return &Server{
		Register:    make(chan *Client, 1000),
		Unregister:  make(chan *Client, 1000),
		Heartbeat:   make(chan *Client, 1000),
		ConnManager: connManage,
	}
}

// Start starts the server.
func (c *Server) Start() {
	defer recover()
	for {
		select {
		case Server := <-c.Register:
			c.ConnManager.Register(Server)

		case Server := <-c.Unregister:
			c.ConnManager.Unregister(Server)

		case Server := <-c.Heartbeat:
			c.ConnManager.Heartbeat(Server)
		}
	}
}
