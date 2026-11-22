package profile

import (
	"encoding/json"
	"testing"
)

func mustMarshal(t *testing.T, s Subscription) []byte {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
