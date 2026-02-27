package ssh

import (
	"context"

	"github.com/pkg/sftp"
)

type Connection interface {
	SftpCli() *sftp.Client
	Exec(ctx context.Context, cmd string) (stdout string, err error)
	Close()
}
