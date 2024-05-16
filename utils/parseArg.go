package utils

import (
	"errors"
	"fmt"
)

func checkArgValidity(arg string, flags []string) bool {
	for _, flag := range flags {
		if arg == flag {
			return true
		}
	}
	return false
}

func checkMapValidity(argMap map[string]string) error {
	if _, isLeader := argMap["leader"]; isLeader {
		if _, hasMasterNode := argMap["masterNode"]; hasMasterNode {
			return errors.New("cannot be both leader and have master node")
		}
	}

	if _, hasPath := argMap["path"]; !hasPath {
		return errors.New("please provide the db log path using --path")
	}

	return nil
}

func ParseCli(args []string) (map[string]string, error) {
	if len(args)%2 != 1 {
		return nil, errors.New("arguments should be in flag-value pairs")
	}

	argMap := make(map[string]string)
	flags := []string{"--masterNode", "--port", "--leader", "--path"}

	for i := 1; i < len(args); i += 2 {
		arg := args[i]
		if !checkArgValidity(arg, flags) {
			return nil, fmt.Errorf("invalid argument: %s", arg)
		}
		if i+1 < len(args) {
			argMap[arg[2:]] = args[i+1]
		} else {
			return nil, fmt.Errorf("value missing for argument: %s", arg)
		}
	}

	if err := checkMapValidity(argMap); err != nil {
		return nil, err
	}

	return argMap, nil
}
