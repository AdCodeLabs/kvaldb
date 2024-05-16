package nodes

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/adcodelabs/kvaldb/nodes/types"
	"log"
	"net"
	"time"
)

type TCPServer struct {
	Serv    net.Listener
	connStr string
}

func NewTcpServer(connStr string) *TCPServer {
	return &TCPServer{
		Serv:    nil,
		connStr: connStr,
	}
}

func (s *TCPServer) Init() (*TCPServer, error) {
	var err error
	s.Serv, err = net.Listen("tcp", s.connStr)
	if err != nil {
		return nil, err
	}

	log.Printf("TCP SERVER CREATED %s", s.connStr)
	return s, nil
}

func (s *TCPServer) HandleConnection(con net.Conn, errCh chan<- error, nodeMap *map[string]time.Time) {
	buf := make([]byte, 1024)

	reqLen, err := con.Read(buf)
	if err != nil {
		errCh <- err
	}

	fmt.Println("Received data:", string(buf[:reqLen]))
	var response types.Message
	if err := json.Unmarshal(buf[:reqLen], &response); err != nil {
		log.Printf("error while deserializing message...")
	}

	if _, ok := (*nodeMap)[response.Whom]; !ok {
		(*nodeMap)[response.Whom] = time.Now()
	}

	var buffer bytes.Buffer
	for node, _ := range *nodeMap {
		buffer.WriteString(fmt.Sprintf(",%s", node))
	}

	byteSlice := buffer.Bytes()
	_, err = con.Write(byteSlice)

	if err != nil {
		return
	}

	err = con.Close()
	if err != nil {
		return
	}
}
