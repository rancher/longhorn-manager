package engineapi

import (
	"testing"

	"github.com/longhorn/longhorn-manager/types"

	. "gopkg.in/check.v1"
)

var (
	VolumeName           = "vol"
	VolumeSize     int64 = 10 * 1024 * 1024 * 1024
	ControllerAddr       = "ip-controller-" + VolumeName
	Replica1Addr         = "ip-replica1-" + VolumeName
	Replica2Addr         = "ip-replica2-" + VolumeName
	Replica3Addr         = "ip-replica3-" + VolumeName
)

func Test(t *testing.T) { TestingT(t) }

type TestSuite struct {
}

var _ = Suite(&TestSuite{})

func (s *TestSuite) TestBasic(c *C) {
	var err error
	coll := NewEngineSimulatorCollection()

	sim, err := coll.GetEngineSimulator(VolumeName)
	c.Assert(err, NotNil)

	req := &EngineSimulatorRequest{
		VolumeName:     VolumeName,
		VolumeSize:     VolumeSize,
		ControllerAddr: ControllerAddr,
		ReplicaAddrs: []string{
			Replica1Addr, Replica2Addr,
		},
	}

	err = coll.CreateEngineSimulator(req)
	c.Assert(err, IsNil)

	err = coll.CreateEngineSimulator(req)
	c.Assert(err, ErrorMatches, "duplicate simulator.*")

	sim, err = coll.GetEngineSimulator(VolumeName)
	c.Assert(err, IsNil)

	replicas, err := sim.ReplicaList()
	c.Assert(err, IsNil)
	c.Assert(replicas, HasLen, 2)
	c.Assert(replicas[Replica1Addr].Mode, Equals, types.ReplicaModeRW)
	c.Assert(replicas[Replica2Addr].Mode, Equals, types.ReplicaModeRW)

	err = sim.ReplicaRemove(Replica2Addr)
	c.Assert(err, IsNil)

	replicas, err = sim.ReplicaList()
	c.Assert(err, IsNil)
	c.Assert(replicas, HasLen, 1)
	c.Assert(replicas[Replica1Addr].Mode, Equals, types.ReplicaModeRW)

	err = sim.ReplicaAdd(Replica3Addr, false)
	replicas, err = sim.ReplicaList()
	c.Assert(err, IsNil)
	c.Assert(replicas, HasLen, 2)
	c.Assert(replicas[Replica1Addr].Mode, Equals, types.ReplicaModeRW)
	c.Assert(replicas[Replica3Addr].Mode, Equals, types.ReplicaModeRW)

	err = coll.DeleteEngineSimulator(VolumeName)
	c.Assert(err, IsNil)
}
