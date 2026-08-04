package nntp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
)

func TestIsBenignDisconnect(t *testing.T) {
	benign := []error{
		context.Canceled,
		net.ErrClosed,
		fmt.Errorf("read tcp 192.168.1.50:1470->81.171.92.219:563: use of closed network connection"),
		fmt.Errorf("wrapped: %w", context.Canceled),
		errors.New("context canceled"),
	}
	for _, err := range benign {
		if !IsBenignDisconnect(err) {
			t.Errorf("expected benign: %v", err)
		}
	}

	notBenign := []error{
		nil,
		errors.New("430 No Such Article"),
		context.DeadlineExceeded,
		errors.New("connection refused"),
		errors.New("EOF"),
	}
	for _, err := range notBenign {
		if IsBenignDisconnect(err) {
			t.Errorf("expected NOT benign: %v", err)
		}
	}
}
