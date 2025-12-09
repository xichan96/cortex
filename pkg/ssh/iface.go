package ssh

import "github.com/pkg/sftp"

type Connection interface {
	SftpCli() *sftp.Client
	Exec(cmd string) (stdout string, err error)
	Close()
}
