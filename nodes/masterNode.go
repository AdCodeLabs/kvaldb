package nodes

import (
	"fmt"
	"github.com/adcodelabs/kvaldb/utils"
	"log"
	"sync"
	"time"
)

func (n *Node) initMasterNode(terminationCh chan bool) error {
	log.Printf("master node initialized...")

	var wg sync.WaitGroup
	wg.Add(1)
	n.nodeType = utils.Leader
	n.currMaster = n.tcpServer.connStr
	errCh := make(chan error)

	// accept results from followers and candidates
	go n.AcceptMessages(errCh)
	// check if follower nodes are still active, if not delete from nodeMap
	go n.checkFollowerNodes(&wg)

	select {
	case err := <-errCh:
		if err != nil {
			return err
		}
	}

	select {
	case term := <-terminationCh:
		if !term {
			return nil
		}
	}

	wg.Wait()

	return nil
}

func (n *Node) checkFollowerNodes(wg *sync.WaitGroup) {
	defer wg.Done()
	for {
		fmt.Println(n.CurrentNodeList)
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
