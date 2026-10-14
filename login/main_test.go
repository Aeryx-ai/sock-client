package login

import (
	"os"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	doneGrace = 50 * time.Millisecond
	os.Exit(m.Run())
}
