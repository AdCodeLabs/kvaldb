package utils

type NodeType string
type MessageType string

const (
	Leader      NodeType    = "LEADER"
	Follower    NodeType    = "FOLLOWER"
	Candidate   NodeType    = "CANDIDATE"
	HeartBeat   MessageType = "HEARTBEAT"
	SynMessage  MessageType = "SYNMESSAGE"
	VoteMessage MessageType = "VOTEREQUEST"
	VoteAccept  MessageType = "VOTEACCEPT"
)
