package smartevse

import (
	"victron_smartevse/internal/testhelper"
)

type fakeDBusConn = testhelper.FakeDBusConn

func newFakeDBusConn() *fakeDBusConn {
	return testhelper.NewFakeDBusConn()
}
