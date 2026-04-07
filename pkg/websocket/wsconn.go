package websocket

import (
	"container/list"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/xichan96/cortex/pkg/logger"
)

const (
	TypeSys       = "sys"
	TypeHeartbeat = "heartbeat"
)

type RecvMsg struct {
	Type string      `json:"type"`
	Data interface{} `json:"data"`
}

type SendMsg struct {
	Status  int8   `json:"status"`
	MsgFrom string `json:"msg_from"`
	Msg     string `json:"msg"`
}

func (s *SendMsg) DoSend(cli *Client) {
	data, _ := json.Marshal(s)
	cli.SendMsg(data)
}

var WsServer *Server

func init() {
	connManage := NewWsConnManage()
	WsServer = NewServer(connManage)
	go WsServer.Start()
}

func connID(cli *Client) string {
	return fmt.Sprintf("%p", cli)
}

func sessionID(cli *Client) string {
	if cli.User != "" {
		return cli.User
	}
	return connID(cli)
}

type sessionConnSet struct {
	mu sync.RWMutex
	m  map[string]*Client
}

type WsConnManage struct {
	expirationTime int64
	clients        sync.Map
	sessionConns   sync.Map
	clientsLock    sync.RWMutex
	connList       *list.List
	connElementMap sync.Map
}

func NewWsConnManage() ConnManagerIface {
	return &WsConnManage{
		expirationTime: 120,
		connList:       list.New(),
	}
}

func (w *WsConnManage) removeConnFromSessionLocked(sid, id string) {
	v, ok := w.sessionConns.Load(sid)
	if !ok {
		return
	}
	set := v.(*sessionConnSet)
	set.mu.Lock()
	delete(set.m, id)
	empty := len(set.m) == 0
	set.mu.Unlock()
	if empty {
		w.sessionConns.Delete(sid)
	}
}

func (w *WsConnManage) Register(cli *Client) {
	w.clientsLock.Lock()
	defer w.clientsLock.Unlock()
	id := connID(cli)
	sid := sessionID(cli)
	w.clients.Store(id, cli)
	placeHolder := &sessionConnSet{m: make(map[string]*Client)}
	v, _ := w.sessionConns.LoadOrStore(sid, placeHolder)
	set := v.(*sessionConnSet)
	set.mu.Lock()
	set.m[id] = cli
	set.mu.Unlock()
	if oldEl, ok := w.connElementMap.Load(id); ok {
		w.connList.MoveToFront(oldEl.(*list.Element))
	} else {
		element := w.connList.PushFront(id)
		w.connElementMap.Store(id, element)
	}
}

func (w *WsConnManage) Unregister(cli *Client) {
	w.clientsLock.Lock()
	defer w.clientsLock.Unlock()
	id := connID(cli)
	sid := sessionID(cli)
	w.clients.Delete(id)
	w.removeConnFromSessionLocked(sid, id)
	if element, ok := w.connElementMap.Load(id); ok {
		w.connList.Remove(element.(*list.Element))
		w.connElementMap.Delete(id)
	}
	cli.Socket.Close()
}

func (w *WsConnManage) Heartbeat(cli *Client) {
	w.clientsLock.Lock()
	defer w.clientsLock.Unlock()
	cli.AccessTime = time.Now().Unix()
	id := connID(cli)
	if sElement, ok := w.connElementMap.Load(id); ok {
		w.connList.MoveToFront(sElement.(*list.Element))
	}
}

func (w *WsConnManage) ClientsOfSession(sessionID string) []*Client {
	if sessionID == "" {
		return nil
	}
	v, ok := w.sessionConns.Load(sessionID)
	if !ok {
		return nil
	}
	set := v.(*sessionConnSet)
	set.mu.RLock()
	defer set.mu.RUnlock()
	out := make([]*Client, 0, len(set.m))
	for _, cli := range set.m {
		out = append(out, cli)
	}
	return out
}

func (w *WsConnManage) BroadcastSession(sessionID string, msg []byte) {
	for _, cli := range w.ClientsOfSession(sessionID) {
		cli.SendMsg(msg)
	}
}

func (w *WsConnManage) DestroyTask() {
	defer recover()
	c := cron.New()
	_, err := c.AddFunc("@every 15s", func() {
		w.clientsLock.Lock()
		defer w.clientsLock.Unlock()
		for i := w.connList.Back(); i != nil; {
			prev := i.Prev()
			id := i.Value.(string)
			if sCli, ok := w.clients.Load(id); !ok {
				if element, ok := w.connElementMap.Load(id); ok {
					w.connList.Remove(element.(*list.Element))
					w.connElementMap.Delete(id)
				}
			} else {
				cli := sCli.(*Client)
				if time.Now().Unix()-cli.AccessTime > w.expirationTime {
					w.clients.Delete(id)
					sid := sessionID(cli)
					w.removeConnFromSessionLocked(sid, id)
					if element, ok2 := w.connElementMap.Load(id); ok2 {
						w.connList.Remove(element.(*list.Element))
						w.connElementMap.Delete(id)
					}
					cli.Socket.Close()
					logger.Info("heartbeat timeout, disconnect",
						slog.String("conn", id),
						slog.String("session_id", sid),
						slog.Int("count", w.connList.Len()),
					)
				}
			}
			i = prev
		}
	})
	if err != nil {
		logger.Error("cron error", slog.Attr{Key: "error", Value: slog.StringValue(err.Error())})
		return
	}
	c.Start()
	defer c.Stop()
	select {}
}

func ProcessMsg(cli *Client, message []byte) {
	recvMsg := RecvMsg{}
	err := json.Unmarshal(message, &recvMsg)
	if err != nil {
		msg := SendMsg{
			Status:  -1,
			MsgFrom: TypeSys,
			Msg:     "消息格式错误",
		}
		msg.DoSend(cli)
		return
	}
	switch recvMsg.Type {
	case TypeHeartbeat:
		WsServer.Heartbeat <- cli
		msg := SendMsg{
			Status:  0,
			MsgFrom: TypeHeartbeat,
		}
		msg.DoSend(cli)
	}
}
