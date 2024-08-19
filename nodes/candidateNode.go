package nodes

import (
	"encoding/json"
	"fmt"
	"github.com/adcodelabs/kvaldb/nodes/types"
	"github.com/adcodelabs/kvaldb/utils"
	"log"
	"net"
	"sync"
	"time"
)

func (n *Node) initCandidateNode() error {
	log.Printf("initializing candidate node...")
	if err := n.startNewElection(); err != nil {
		return err
	}
	return nil
}

func (n *Node) voteRequest(node string, wg *sync.WaitGroup, votes chan<- bool) {
	defer wg.Done()

	conn, err := net.Dial("tcp", node)
	if err != nil {
		log.Printf("Error connecting to %s: %v", node, err)
		votes <- false
		return
	}

	defer func() {
		err = conn.Close()
		if err != nil {
			return
		}
	}()

	message := types.Message{Whom: n.tcpServer.connStr, MType: utils.VoteMessage, Body: ""}
	msg, err := json.Marshal(message)
	if err != nil {
		log.Printf("Error marshalling message for %s: %v", node, err)
		votes <- false
		return
	}

	_, err = conn.Write(msg)
	if err != nil {
		log.Printf("Error sending message to %s: %v", node, err)
		votes <- false
		return
	}

	readBuf := make([]byte, 1024)
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	readLen, err := conn.Read(readBuf)
	if err != nil {
		log.Printf("Error reading response from %s: %v", node, err)
		votes <- false
		return
	}

	log.Printf("Received response from %s: %s", node, string(readBuf[:readLen]))
	votes <- true

}

func (n *Node) startNewElection() error {
	log.Println("STARTING NEW ELECTION...")

	voteCounter := 0
	totalNodes := len(n.CurrentNodeList)
	requiredVotes := totalNodes / 2
	var wg sync.WaitGroup
	votes := make(chan bool, totalNodes)

	for node := range n.CurrentNodeList {
		wg.Add(1)
		go n.voteRequest(node, &wg, votes)
	}

	wg.Wait()
	close(votes)

	for vote := range votes {
		if vote {
			voteCounter++
			if voteCounter >= requiredVotes {
				n.nodeType = utils.Leader
				log.Println("Election won. Current node is the leader.")
				return nil
			}
		}
	}

	log.Println("Election lost or insufficient votes received.")
	return fmt.Errorf("election failed")
}
