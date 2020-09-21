package reconnect

import "testing"

func TestAtomicBool(t *testing.T) {
	atomicBool := newAtomicBool()

	if atomicBool.Get() != false {
		t.Error("'atomicBool.Get' must be false")
	}

	atomicBool.Set(true)
	if atomicBool.Get() != true {
		t.Error("'atomicBool.Get' must be true")
	}

	atomicBool.Set(false)
	if atomicBool.Get() != false {
		t.Error("'atomicBool.Get' must be false")
	}

	atomicBool.Set(false)
	if atomicBool.Get() != false {
		t.Error("'atomicBool.Get' must be false")
	}
}
