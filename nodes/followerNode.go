package nodes

import (
	"encoding/json"
	"fmt"
	"github.com/adcodelabs/kvaldb/nodes/types"
	"github.com/adcodelabs/kvaldb/utils"
	"log"
	"net"
	"strings"
	"time"
)

func (n *Node) initFollowerNode(terminationCh chan bool) error {
	n.nodeType = utils.Follower
	errCh := make(chan error)
	hbeatCh := make(chan bool)

	if err := n.sendSyncMessage(n.currMaster); err != nil {
		return err
	}

	// accept message from master and candidate nodes, mainly used for askForMaster function
	go n.AcceptMessages(errCh)
	// send heart beat to master node
	go n.sendHeartbeat(n.currMaster, errCh)
	select {
	case err := <-errCh:
		if err != nil {
			log.Printf("Error with hearbeats: %s", err)
		}
	}

	// check last heart beat from each follower node
	go n.checkLastHeartbeat(n.heartBeatDur, n.lastCheck, hbeatCh)
	select {
	case uptime := <-hbeatCh:
		if !uptime {
			log.Printf("no response from master node...")
			n.nodeType = utils.Candidate
			return nil
		}
	}

	select {
	case term := <-terminationCh:
		if !term {
			return nil
		}
	}

	return nil
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

	messageStruct := types.Message{MType: utils.SynMessage, Whom: n.tcpServer.connStr, Body: ""}
	mes, _ := json.Marshal(messageStruct)
	_, err = connection.Write(mes)
	if err != nil {
		return err
	}
	return nil
}

func (n *Node) sendHeartbeat(connStr string, errCh chan<- error) {
	for {
		time.Sleep(n.heartBeatDur * time.Millisecond)
		func() {
			conn, err := net.Dial("tcp", connStr)

			if err != nil {
				errCh <- err
				return
			}
			message := types.Message{Whom: n.tcpServer.connStr, MType: utils.HeartBeat, Body: ""}
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
