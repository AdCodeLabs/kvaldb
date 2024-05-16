package nodes

import (
	"encoding/json"
	"fmt"
	"github.com/adcodelabs/kvaldb/nodes/types"
	"github.com/adcodelabs/kvaldb/utils"
	"log"
	"math/rand"
	"net"
	"strings"
	"sync"
	"time"
)

type NodeManager interface {
	Init() error
}

type Node struct {
	nodeType        utils.NodeType
	currMaster      string
	tcpServer       *TCPServer
	lastCheck       time.Time
	heartBeatDur    time.Duration
	CurrentNodeList map[string]time.Time
}

func NewNode(isMaster bool, port string, currMaster string) (*Node, error) {
	var nodeType utils.NodeType
	if isMaster {
		nodeType = utils.Leader
	} else {
		nodeType = utils.Follower
	}

	connStr := fmt.Sprintf("0.0.0.0:%s", port)
	tcpServer, err := NewTcpServer(connStr).Init()
	if err != nil {
		return nil, err
	}

	return &Node{
		nodeType:        nodeType,
		tcpServer:       tcpServer,
		currMaster:      currMaster,
		lastCheck:       time.Now(),
		heartBeatDur:    time.Duration(rand.Intn(1000)),
		CurrentNodeList: make(map[string]time.Time),
	}, nil
}

func (n *Node) Init() error {
	log.Printf("HEARTBEAT CHECK DURATION IS SET TO %d", n.heartBeatDur)
	terminationCh := make(chan bool)
	errCh := make(chan error)

	go func(termChan chan bool) {
		lastType := n.nodeType
		if n.nodeType == utils.Leader {
			if err := n.initMasterNode(terminationCh); err != nil {
				errCh <- err
			}
		} else if n.nodeType == utils.Follower {
			if err := n.initFollowerNode(); err != nil {
				errCh <- err
			}
		}

		for {
			if lastType == n.nodeType {
				continue
			} else {
				log.Printf("Node type is changed...")
				if n.nodeType != utils.Leader {
					terminationCh <- true
				}
				terminationCh = make(chan bool)
				lastType = n.nodeType
				if n.nodeType == utils.Leader {
					if err := n.initMasterNode(terminationCh); err != nil {
						log.Print(err)
					}
				} else if n.nodeType == utils.Follower {
					if err := n.initFollowerNode(); err != nil {
						log.Print(err)
					}
				}
			}

		}
	}(terminationCh)

	err := <-errCh
	if err != nil {
		return err
	}

	return nil
}

func (n *Node) initMasterNode(terminationCh chan bool) error {
	log.Printf("master node initialized...")
	var wg sync.WaitGroup
	wg.Add(1)
	n.nodeType = utils.Leader
	errCh := make(chan error)

	go n.AcceptMessages(errCh)
	go n.checkFollowerNodes(&wg)
	err := <-errCh
	if err != nil {
		return err
	}

	wg.Wait()

	term := <-terminationCh
	if !term {
		return nil
	}
	return nil
}

func (n *Node) initFollowerNode() error {
	n.nodeType = utils.Follower
	errCh := make(chan error)
	hbeatCh := make(chan bool)

	if err := n.sendSyncMessage(n.currMaster); err != nil {
		return err
	}

	go n.SendHeartbeat(n.currMaster, errCh)
	err := <-errCh
	if err != nil {
		log.Printf("Error with hearbeats: %s", err)
	}

	go n.CheckLastHeartbeat(n.heartBeatDur, n.lastCheck, hbeatCh)
	uptime := <-hbeatCh
	if !uptime {
		n.StartNewElection()
		return nil
	}

	return nil
}

func (n *Node) initCandidateNode() {

}

func (n *Node) sendSyncMessage(connStr string) error {
	connection, err := net.Dial("tcp", connStr)
	defer func() {
		if err := connection.Close(); err != nil {
			fmt.Println("Error closing the connection:", err)
		}
	}()

	if err != nil {
		return err
	}
	messageStruct := types.Message{MType: utils.SynMessage, Whom: n.tcpServer.connStr}
	mes, _ := json.Marshal(messageStruct)
	_, err = connection.Write(mes)
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
		go n.tcpServer.HandleConnection(connection, errCh, &n.CurrentNodeList)
		fmt.Println(n.CurrentNodeList)
	}
}

func (n *Node) SendHeartbeat(connStr string, errCh chan<- error) {
	for {
		time.Sleep(n.heartBeatDur * time.Millisecond)
		func() {
			conn, err := net.Dial("tcp", connStr)

			if err != nil {
				errCh <- err
				return
			}
			message := types.Message{Whom: n.tcpServer.connStr, MType: utils.HeartBeat}
			msg, _ := json.Marshal(message)
			defer func() {
				if err := conn.Close(); err != nil {
					fmt.Println("Error closing the connection:", err)
				}
			}()

			_, err = conn.Write(msg)
			if err != nil {
				fmt.Println(err)
				errCh <- err
			}

			readBuf := make([]byte, 1024)
			readLen, err := conn.Read(readBuf)
			if err != nil {
				return
			}
			if readLen > 100 {
				n.lastCheck = time.Now()
			}
			log.Printf("Leader message: %s", string(readBuf[1:readLen]))
			listOfNodes := strings.Split(string(readBuf[1:readLen]), ",")
			for _, val := range listOfNodes {
				n.CurrentNodeList[val] = time.Now()
			}

		}()
	}
}

func (n *Node) CheckLastHeartbeat(heartBeatDur time.Duration, lastCheck time.Time, heartbeatCh chan<- bool) {
	for {
		if time.Now().Sub(lastCheck) > heartBeatDur {
			heartbeatCh <- false
		}
	}
}

func (n *Node) checkFollowerNodes(wg *sync.WaitGroup) {
	defer wg.Done()
	for {
		time.Sleep(time.Second)
		for node, lastCheckTime := range n.CurrentNodeList {
			go func(node string) {
				if time.Now().Sub(lastCheckTime) > time.Second {
					delete(n.CurrentNodeList, node)
					log.Printf("cannot reach: %s", node)
				}
			}(node)
		}
	}
}

func (n *Node) voteRequest() {
	for nodeConnStr, _ := range n.CurrentNodeList {
		go func() {
			conn, err := net.Dial("tcp", nodeConnStr)
			if err != nil {
				log.Print(err)
			}
			defer func() {
				err = conn.Close()
				if err != nil {
					return
				}
			}()

			message := types.Message{Whom: n.tcpServer.connStr, MType: utils.VoteMessage}
			msg, _ := json.Marshal(message)
			_, err = conn.Write(msg)
			if err != nil {
				log.Print(err)
			}

			readBuf := make([]byte, 1024)
			readLen, err := conn.Read(readBuf)
			log.Printf("Leader message: %s", string(readBuf[1:readLen]))
		}()
	}
}

func (n *Node) StartNewElection() {
	log.Printf("STARTING NEW ELECTION...")
	n.nodeType = utils.Leader

}
