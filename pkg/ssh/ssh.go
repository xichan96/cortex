package ssh

import (
	"bufio"
	"context"
	"fmt"
	"io/ioutil"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/errors"
	"github.com/pkg/sftp"
	"github.com/xichan96/cortex/pkg/logger"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

type Cfg struct {
	Username    string        `json:"username"`
	Password    string        `json:"password"`
	Address     string        `json:"address"`
	Port        int           `json:"port"`
	PrivateKey  string        `json:"-"`
	KeyFile     string        `json:"-"`
	AgentSocket string        `json:"-"`
	Timeout     time.Duration `json:"timeout"`
	Bastion     string        `json:"-"`
	BastionPort int           `json:"-"`
	BastionUser string        `json:"-"`
}

const socketEnvPrefix = "env:"

type connection struct {
	cfg        *Cfg
	mu         sync.Mutex
	sftpclient *sftp.Client
	sshclient  *ssh.Client
	ctx        context.Context
	cancel     context.CancelFunc
}

func NewConnection(cfg Cfg) (Connection, error) {
	cfg, err := validateOptions(cfg)
	if err != nil {
		return nil, errors.Wrap(err, "Failed to validate ssh connection parameters")
	}

	authMethods := make([]ssh.AuthMethod, 0)

	if len(cfg.Password) > 0 {
		authMethods = append(authMethods, ssh.Password(cfg.Password))
	}

	if len(cfg.PrivateKey) > 0 {
		signer, parseErr := ssh.ParsePrivateKey([]byte(cfg.PrivateKey))
		if parseErr != nil {
			return nil, errors.Wrap(parseErr, "The given SSH key could not be parsed")
		}
		authMethods = append(authMethods, ssh.PublicKeys(signer))
	}

	if len(cfg.AgentSocket) > 0 {
		addr := cfg.AgentSocket

		if strings.HasPrefix(cfg.AgentSocket, socketEnvPrefix) {
			envName := strings.TrimPrefix(cfg.AgentSocket, socketEnvPrefix)

			if envAddr := os.Getenv(envName); len(envAddr) > 0 {
				addr = envAddr
			}
		}

		socket, dialErr := net.Dial("unix", addr)
		if dialErr != nil {
			return nil, errors.Wrapf(dialErr, "could not open socket %q", addr)
		}

		agentClient := agent.NewClient(socket)

		signers, signersErr := agentClient.Signers()
		if signersErr != nil {
			_ = socket.Close()
			return nil, errors.Wrap(signersErr, "error when creating signer for SSH agent")
		}

		authMethods = append(authMethods, ssh.PublicKeys(signers...))
	}

	sshConfig := &ssh.ClientConfig{
		User:            cfg.Username,
		Timeout:         cfg.Timeout,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	targetHost := cfg.Address
	targetPort := strconv.Itoa(cfg.Port)

	if cfg.Bastion != "" {
		targetHost = cfg.Bastion
		targetPort = strconv.Itoa(cfg.BastionPort)
		sshConfig.User = cfg.BastionUser
	}

	endpoint := net.JoinHostPort(targetHost, targetPort)

	client, err := ssh.Dial("tcp", endpoint, sshConfig)
	if err != nil {
		return nil, errors.Wrapf(err, "could not establish connection to %s", endpoint)
	}

	ctx, cancelFn := context.WithCancel(context.Background())
	sshConn := &connection{
		ctx:    ctx,
		cfg:    &cfg,
		cancel: cancelFn,
	}

	if cfg.Bastion == "" {
		sshConn.sshclient = client
		sftpClient, err := sftp.NewClient(sshConn.sshclient)
		if err != nil {
			return nil, errors.Wrapf(err, "new sftp client failed: %v", err)
		}
		sshConn.sftpclient = sftpClient
		return sshConn, nil
	}

	endpointBehindBastion := net.JoinHostPort(cfg.Address, strconv.Itoa(cfg.Port))

	conn, err := client.Dial("tcp", endpointBehindBastion)
	if err != nil {
		return nil, errors.Wrapf(err, "could not establish connection to %s", endpointBehindBastion)
	}

	sshConfig.User = cfg.Username
	ncc, chans, reqs, err := ssh.NewClientConn(conn, endpointBehindBastion, sshConfig)
	if err != nil {
		return nil, errors.Wrapf(err, "could not establish connection to %s", endpointBehindBastion)
	}

	sshConn.sshclient = ssh.NewClient(ncc, chans, reqs)
	sftpClient, err := sftp.NewClient(sshConn.sshclient)
	if err != nil {
		return nil, errors.Wrapf(err, "new sftp client failed: %v", err)
	}
	sshConn.sftpclient = sftpClient
	return sshConn, nil
}

func validateOptions(cfg Cfg) (Cfg, error) {
	if len(cfg.Username) == 0 {
		return cfg, errors.New("No username specified for SSH connection")
	}

	if len(cfg.Address) == 0 {
		return cfg, errors.New("No address specified for SSH connection")
	}

	if len(cfg.Password) == 0 && len(cfg.PrivateKey) == 0 && len(cfg.KeyFile) == 0 && len(cfg.AgentSocket) == 0 {
		return cfg, errors.New("Must specify at least one of password, private key, keyfile or agent socket")
	}

	if len(cfg.PrivateKey) == 0 && len(cfg.KeyFile) > 0 {
		content, err := ioutil.ReadFile(cfg.KeyFile)
		if err != nil {
			return cfg, errors.Wrapf(err, "Failed to read keyfile %q", cfg.KeyFile)
		}

		cfg.PrivateKey = string(content)
		cfg.KeyFile = ""
	}

	if cfg.Port <= 0 {
		cfg.Port = 22
	}

	if cfg.BastionPort <= 0 {
		cfg.BastionPort = 22
	}

	if cfg.BastionUser == "" {
		cfg.BastionUser = cfg.Username
	}

	if cfg.Timeout == 0 {
		cfg.Timeout = 15 * time.Second
	}

	return cfg, nil
}

func (c *connection) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.sshclient == nil && c.sftpclient == nil {
		return
	}
	c.cancel()

	if c.sshclient != nil {
		c.sshclient.Close()
		c.sshclient = nil
	}
	if c.sftpclient != nil {
		c.sftpclient.Close()
		c.sftpclient = nil
	}
}

func (c *connection) session() (*ssh.Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.sshclient == nil {
		return nil, errors.New("connection closed")
	}

	sess, err := c.sshclient.NewSession()
	if err != nil {
		return nil, err
	}

	modes := ssh.TerminalModes{
		ssh.ECHO:          0,     // disable echoing
		ssh.TTY_OP_ISPEED: 14400, // input speed = 14.4kbaud
		ssh.TTY_OP_OSPEED: 14400, // output speed = 14.4kbaud
	}

	err = sess.RequestPty("xterm", 100, 50, modes)
	if err != nil {
		return nil, err
	}

	return sess, nil
}

func (c *connection) Exec(cmd string) (stdout string, err error) {
	sess, err := c.session()
	if err != nil {
		return "", errors.Wrap(err, "failed to get SSH session")
	}
	defer sess.Close()

	exitCode := 0

	in, _ := sess.StdinPipe()
	out, _ := sess.StdoutPipe()

	err = sess.Start(strings.TrimSpace(cmd))
	if err != nil {
		exitCode = -1
		if exitErr, ok := err.(*ssh.ExitError); ok {
			exitCode = exitErr.ExitStatus()
		}
		logger.NewLogger().Info("exitCode", slog.Int("exitCode", exitCode))
		return "", err
	}

	var (
		output []byte
		line   = ""
		r      = bufio.NewReader(out)
	)

	for {
		b, err := r.ReadByte()
		if err != nil {
			break
		}

		output = append(output, b)

		if b == byte('\n') {
			line = ""
			continue
		}

		line += string(b)

		if (strings.HasPrefix(line, "[sudo] password for ") || strings.HasPrefix(line, "Password")) && strings.HasSuffix(line, ": ") {
			_, err = in.Write([]byte(c.cfg.Password + "\n"))
			if err != nil {
				break
			}
		}
	}
	err = sess.Wait()
	if err != nil {
		exitCode = -1
		if exitErr, ok := err.(*ssh.ExitError); ok {
			exitCode = exitErr.ExitStatus()
		}
	}
	outStr := strings.TrimPrefix(string(output), fmt.Sprintf("[sudo] password for %s:", c.cfg.Username))

	// preserve original error
	return strings.TrimSpace(outStr), errors.Wrapf(err, "Failed to exec command: %s \n%s", cmd, strings.TrimSpace(outStr))
}

func (c *connection) SftpCli() *sftp.Client {
	return c.sftpclient
}
