package types

import "github.com/adcodelabs/kvaldb/utils"

type Message struct {
	Whom  string
	MType utils.MessageType
}
