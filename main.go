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

	isLeader := false
	if val, exists := argMap["leader"]; exists && strings.ToLower(val) == "true" {
		isLeader = true
	}

	currMaster := ""
	if !isLeader {
		currMaster = argMap["masterNode"]
	}

	node, err := nodes.NewNode(isLeader, argMap["port"], currMaster)
	if err != nil {
		log.Fatalf("Error creating node: %v", err)
	}

	if err = node.Init(); err != nil {
		log.Fatalf("Error initializing node: %v", err)
	}
}
