package server

import (
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type Dialer struct {
	mu    sync.Mutex
	conns map[string]*grpc.ClientConn
}

func NewDialer() *Dialer {
	return &Dialer{conns: make(map[string]*grpc.ClientConn)}
}

// returns a cached connection to addr.
func (d *Dialer) Conn(addr string) (*grpc.ClientConn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if c, ok := d.conns[addr]; ok {
		return c, nil
	}
	c, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	d.conns[addr] = c
	return c, nil
}

// closes all cached connections.
func (d *Dialer) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	var first error
	for _, c := range d.conns {
		if err := c.Close(); err != nil && first == nil {
			first = err
		}
	}
	d.conns = make(map[string]*grpc.ClientConn)
	return first
}
