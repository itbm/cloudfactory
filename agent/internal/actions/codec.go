package actions

import (
	"google.golang.org/protobuf/encoding/protojson"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
)

func marshalWebsite(w *agentv1.Website) ([]byte, error) {
	if w == nil {
		return []byte("{}"), nil
	}
	return protojson.Marshal(w)
}

func unmarshalWebsite(b []byte, w *agentv1.Website) error {
	return protojson.UnmarshalOptions{DiscardUnknown: true}.Unmarshal(b, w)
}
