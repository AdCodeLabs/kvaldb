package kv

import "encoding/json"

// kvaldb.v1.LogEntry.type
const (
	EntryNoop   uint32 = 0
	EntryConfig uint32 = 1
	EntrySet    uint32 = 2
	EntryDelete uint32 = 3
)

type ConfigPayload struct {
	Peers map[string]string `json:"peers"` // node id -> gRPC address
}

type SetPayload struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type DeletePayload struct {
	Key string `json:"key"`
}

func MarshalConfig(peers map[string]string) ([]byte, error) {
	return json.Marshal(ConfigPayload{Peers: peers})
}

func UnmarshalConfig(data []byte) (map[string]string, error) {
	var p ConfigPayload
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	return p.Peers, nil
}

func MarshalSet(key, value string) ([]byte, error) {
	return json.Marshal(SetPayload{Key: key, Value: value})
}

func MarshalDelete(key string) ([]byte, error) {
	return json.Marshal(DeletePayload{Key: key})
}
