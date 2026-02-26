package ws

import "encoding/json"

type Event struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

func (e Event) Marshal() ([]byte, error) {
	return json.Marshal(e)
}
