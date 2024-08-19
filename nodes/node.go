package nodes

import (
	"encoding/json"
	"fmt"
	"github.com/adcodelabs/kvaldb/nodes/types"
	"github.com/adcodelabs/kvaldb/utils"
	"log"
	"math/rand"
	"net"
	"time"
)

type Node struct {
	nodeType        utils.NodeType
	currMaster      string
	tcpServer       *TCPServer
	lastCheck       time.Time
	heartBeatDur    time.Duration
	CurrentNodeList map[string]time.Time
}

func NewNode(socket string, systemNodes []string) (*Node, error) {
	var nodeType utils.NodeType
	if len(systemNodes) == 0 {
		nodeType = utils.Leader
	} else {
		nodeType = utils.Follower
	}

	tcpServer, err := NewTcpServer(socket).Init()
	if err != nil {
		return nil, err
	}
	currMaster := askForMasterNode(socket, systemNodes)

	return &Node{
		nodeType:        nodeType,
		tcpServer:       tcpServer,
		currMaster:      currMaster,
		lastCheck:       time.Now(),
		heartBeatDur:    time.Duration(rand.Intn(1000)),
		CurrentNodeList: make(map[string]time.Time),
	}, nil
}

func askForMasterNode(localSocket string, systemNodes []string) string {
	if len(systemNodes) == 0 {
		return ""
	}
	log.Printf("ASKING FOR MASTER NODE")

	masterNodeCh := make(chan string, len(systemNodes))
	defer close(masterNodeCh)

	for _, node := range systemNodes {
		go func(node string) {
			conn, err := net.Dial("tcp", node)
			if err != nil {
				fmt.Printf("error while getting master node from %s: %v\n", node, err)
				masterNodeCh <- ""
				return
			}
			defer func() {
				if err := conn.Close(); err != nil {
					fmt.Println("Error closing the connection:", err)
				}
			}()

			message := types.Message{Whom: localSocket, MType: utils.GetMaster, Body: ""}
			msg, err := json.Marshal(message)
			if err != nil {
				fmt.Println("Error marshalling message:", err)
				masterNodeCh <- ""
				return
			}

			_, err = conn.Write(msg)
			if err != nil {
				fmt.Println("Error sending message:", err)
				masterNodeCh <- ""
				return
			}

			readBuf := make([]byte, 1024)
			conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			readLen, err := conn.Read(readBuf)
			if err != nil {
				fmt.Println("error while reading result for master node:", err)
				masterNodeCh <- ""
				return
			}

			masterNode := readBuf[0:readLen]
			var messageStruct types.Message
			println(string(masterNode))
			err = json.Unmarshal(masterNode, &messageStruct)
			if err != nil {
				println("error: %v", err)
			}
			fmt.Println(messageStruct)
			masterNodeCh <- messageStruct.Body
		}(node)
	}

	select {
	case masterNode := <-masterNodeCh:
		return masterNode
	case <-time.After(10 * time.Second):
		return ""
	}

}

// this function is called every time when node is initialized or changed its type
func (n *Node) nodeInitializer(nodeType utils.NodeType, terminationCh chan bool) error {
	// nodeType is last node type observed, if it was changed, new type of node is initialized
	if nodeType == utils.Leader {
		if err := n.initMasterNode(terminationCh); err != nil {
			return err
		}
	} else if n.nodeType == utils.Follower {
		if err := n.initFollowerNode(terminationCh); err != nil {
			return err
		}
	} else if n.nodeType == utils.Candidate {
		if err := n.initCandidateNode(); err != nil {
			return err
		}
	}
	return nil
}

// Init - this function checks node type and initializes the node, only called once
func (n *Node) Init() error {
	log.Printf("HEARTBEAT CHECK DURATION IS SET TO %d", n.heartBeatDur)
	terminationCh := make(chan bool)
	errCh := make(chan error)

	lastType := n.nodeType
	if err := n.nodeInitializer(lastType, terminationCh); err != nil {
		return err
	}

	go func() {
		for {
			if lastType == n.nodeType {
				continue
			} else {
				log.Printf("Node type is changed...")
				if n.nodeType == utils.Candidate {
					terminationCh <- true
				}
				lastType = n.nodeType
				log.Printf("current node type is %s", lastType)
				if err := n.nodeInitializer(lastType, terminationCh); err != nil {
					errCh <- err
				}
			}

		}
	}()

	err := <-errCh
	if err != nil {
		return err
	}

	return nil
}

func (n *Node) AcceptMessages(errCh chan<- error) {
	for {
		connection, err := n.tcpServer.Serv.Accept()

		if err != nil {
			errCh <- err
			return
		}
		go n.tcpServer.HandleConnection(connection, errCh, &n.CurrentNodeList, n)
	}
}

// used by follower node to check if master node still alive or not
func (n *Node) checkLastHeartbeat(heartBeatDur time.Duration, lastCheck time.Time, heartbeatCh chan<- bool) {
	for {
		if time.Now().Sub(lastCheck) > heartBeatDur {
			heartbeatCh <- false
		}
	}
}
