package main

import (
	"log"
	"os"
	"strings"

	"github.com/adcodelabs/kvaldb/nodes"
	"github.com/adcodelabs/kvaldb/utils"
)

func main() {
	argMap, err := utils.ParseCli(os.Args)
	if err != nil {
		log.Fatalf("Error parsing CLI arguments: %v", err)
	}

	var n []string
	if len(argMap["nodes"]) != 0 {
		n = strings.Split(argMap["nodes"], ",")
	} else {
		n = make([]string, 0)
	}
	node, err := nodes.NewNode(argMap["socket"], n)
	if err != nil {
		log.Fatalf("Error while initializing the node: %v", err)
	}

	if err = node.Init(); err != nil {
		log.Fatalf("Error initializing node: %v", err)
	}
}
