package reconnect

import "sync/atomic"

type atomicBool struct {
	value int32
}

func newAtomicBool() *atomicBool {
	return &atomicBool{}
}

func (b *atomicBool) Set(value bool) {
	var v int32 = 0
	if value {
		v = 1
	}
	atomic.StoreInt32(&b.value, v)
}

func (b *atomicBool) Get() bool {
	return atomic.LoadInt32(&b.value) != 0
}
