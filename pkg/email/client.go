// Package email ...
package email

import (
	"context"

	"gopkg.in/gomail.v2"
)

// Config ...
type Config struct {
	Address string `json:"address"`
	Name    string `json:"name"`
	Pwd     string `json:"pwd"`
	Host    string `json:"host"`
	Port    int    `json:"port"`
}

type Content struct {
	Title   string `json:"title"`
	Type    string `json:"type"` // text/html, text/plain, text/markdown
	Message string `json:"message"`
}

// Do 发送
func Do(ctx context.Context, cfg *Config, recvUser []string, content *Content) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	m := gomail.NewMessage()
	m.SetHeader("From", m.FormatAddress(cfg.Address, cfg.Name))
	m.SetHeader("To", recvUser...)
	m.SetHeader("Subject", content.Title)
	m.SetBody(content.Type, content.Message)

	d := gomail.NewDialer(cfg.Host, cfg.Port, cfg.Address, cfg.Pwd)

	errChan := make(chan error, 1)
	go func() {
		if err := d.DialAndSend(m); err != nil {
			errChan <- err
		}
		close(errChan)
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errChan:
		return err
	}
}
