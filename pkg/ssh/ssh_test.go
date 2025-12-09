package ssh

import (
	"fmt"
	"testing"
	"time"
)

func TestSSH(t *testing.T) {
	cfg := Cfg{
		Username: "root",
		Password: "123456",
		Address:  "127.0.0.1",
		Port:     22,
		Timeout:  time.Minute,
	}
	conn, err := NewConnection(cfg)
	defer conn.Close()
	if err != nil {
		t.Error(err)
	}
	cmd := "sudo docker ps"
	result, err := conn.Exec(cmd)
	if err != nil {
		t.Error(err)
		return
	}
	fmt.Println(result)
}

func TestContainerSSH(t *testing.T) {
	cfg := Cfg{
		Username: "root",
		Password: "1",
		Address:  "172.17.229.162",
		Port:     2222,
		Timeout:  time.Minute,
	}
	conn, err := NewConnection(cfg)
	defer conn.Close()
	if err != nil {
		t.Error(err)
	}
	cmd := "pwd\nls\nls"
	result, err := conn.Exec(cmd)
	if err != nil {
		t.Error(err)
		return
	}
	fmt.Println(result)
}
