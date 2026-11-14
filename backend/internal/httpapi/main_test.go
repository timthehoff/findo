package httpapi

import (
	"os"
	"testing"

	"github.com/thoff/findo/backend/internal/smbclient"
)

func TestMain(m *testing.M) {
	// Tests connect to fake NAS objects, never the network; the real timeout
	// would make every failed test sleep out its padding.
	smbclient.ConnectTimeout = 0
	os.Exit(m.Run())
}
