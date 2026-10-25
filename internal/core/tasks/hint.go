package tasks

// Hint is the machine-room suggestion for maxAgents. RaiseTo is 0 when there
// is nothing to suggest.
type Hint struct{ RaiseTo int }

// Constants of the hint: one place, pinned by the table test.
const (
	hintCPUsPerAgent  = 4
	hintMemMBPerAgent = 2048
)

// RoomHint suggests raising maxAgents by one when every slot is busy and the
// machine has room: cpus >= 4*(maxAgents+1) and freeMemMB >= 2048*(maxAgents+1). It never
// suggests more than RoomHintCeiling and never when active < maxAgents.
func RoomHint(cpus, freeMemMB, maxAgents, active int) Hint {
	next := maxAgents + 1
	if maxAgents < MinMaxAgents || next > RoomHintCeiling || active < maxAgents {
		return Hint{}
	}
	if cpus >= hintCPUsPerAgent*next && freeMemMB >= hintMemMBPerAgent*next {
		return Hint{RaiseTo: next}
	}
	return Hint{}
}
