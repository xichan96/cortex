package ssh

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestSSH(t *testing.T) {
	if os.Getenv("CORTEX_SSH_TEST") == "" {
		t.Skip("CORTEX_SSH_TEST is not set")
	}
	cfg := Cfg{
		Username: "root",
		Password: "123456",
		Address:  "127.0.0.1",
		Port:     22,
		Timeout:  time.Minute,
	}
	ctx := context.Background()
	conn, err := NewConnection(ctx, cfg)
	if err != nil {
		t.Error(err)
		return
	}
	defer conn.Close()
	cmd := "sudo docker ps"
	result, err := conn.Exec(ctx, cmd)
	if err != nil {
		t.Error(err)
		return
	}
	fmt.Println(result)
}

func TestContainerSSH(t *testing.T) {
	if os.Getenv("CORTEX_SSH_TEST") == "" {
		t.Skip("CORTEX_SSH_TEST is not set")
	}
	cfg := Cfg{
		Username: "root",
		Password: "1",
		Address:  "172.17.229.162",
		Port:     2222,
		Timeout:  time.Minute,
	}
	ctx := context.Background()
	conn, err := NewConnection(ctx, cfg)
	if err != nil {
		t.Error(err)
		return
	}
	defer conn.Close()
	cmd := "pwd\nls\nls"
	result, err := conn.Exec(ctx, cmd)
	if err != nil {
		t.Error(err)
		return
	}
	fmt.Println(result)
}
