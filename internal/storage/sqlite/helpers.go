package sqlite

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

func newID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("fallback-%d", time.Now().UTC().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func intToBool(v int) bool {
	return v != 0
}
