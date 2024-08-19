package nodes

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/adcodelabs/kvaldb/nodes/types"
	"github.com/adcodelabs/kvaldb/utils"
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

func (s *TCPServer) HandleConnection(con net.Conn, errCh chan<- error, nodeMap *map[string]time.Time, node *Node) {
	buf := make([]byte, 1024)

	reqLen, err := con.Read(buf)
	if err != nil {
		errCh <- err
	}

	var response types.Message
	if err := json.Unmarshal(buf[:reqLen], &response); err != nil {
		log.Printf("error while deserializing message...")
	}

	if response.MType == utils.HeartBeat || response.MType == utils.SynMessage {
		err := s.hearBeatHandler(con, nodeMap, response)
		if err != nil {
			errCh <- err
		}
	} else if response.MType == utils.GetMaster {
		err := s.getMasterHandler(con, node)
		if err != nil {
			errCh <- err
		}
	} else if response.MType == utils.ReturnMaster {
		node.currMaster = response.Body
	} else if response.MType == utils.VoteMessage {
		err := s.votingRequestHandler(con, node)
		if err != nil {
			errCh <- err
		}
	}

	err = con.Close()
	if err != nil {
		return
	}
}

func (s *TCPServer) hearBeatHandler(con net.Conn, nodeMap *map[string]time.Time, response types.Message) error {
	if _, ok := (*nodeMap)[response.Whom]; !ok {
		(*nodeMap)[response.Whom] = time.Now()
	}

	var buffer bytes.Buffer
	for node, _ := range *nodeMap {
		buffer.WriteString(fmt.Sprintf(",%s", node))
	}

	byteSlice := buffer.Bytes()
	_, err := con.Write(byteSlice)

	if err != nil {
		return err
	}
	return nil
}

func (s *TCPServer) getMasterHandler(con net.Conn, node *Node) error {
	resultMessage := types.Message{MType: utils.ReturnMaster, Whom: node.tcpServer.connStr, Body: node.currMaster}
	rm, _ := json.Marshal(resultMessage)
	_, err := con.Write(rm)
	if err != nil {
		return err
	}
	return nil
}

func (s *TCPServer) returnMasterHandler() {

}

func (s *TCPServer) votingRequestHandler(con net.Conn, node *Node) error {
	resultMessage := types.Message{MType: utils.VoteAccept, Whom: node.tcpServer.connStr, Body: ""}
	rm, _ := json.Marshal(resultMessage)
	_, err := con.Write(rm)
	if err != nil {
		return err
	}
	return nil
}

func (s *TCPServer) votingAcceptanceHandler(con net.Conn, node *Node) error {
	return nil
}

func (s *TCPServer) newMasterHandler(con net.Conn, node *Node) error {
	return nil
}
