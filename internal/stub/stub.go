package stub

import (
	_ "embed"
	"sync"
	"sync/atomic"
	"unsafe"
)

type HanzoImports interface {
	Hz_close(m *Module, l0 int32)
	Hz_open(m *Module, l0 int32, l1 int32, l2 int32, l3 int32, l4 int32) int32
	Hz_reply(m *Module, l0 int32, l1 int32, l2 int32) int32
	Hz_write(m *Module, l0 int32, l1 int32, l2 int32) int32
	Hz_end(m *Module, l0 int32, l1 int32, l2 int32) int32
	Hz_head(m *Module, l0 int32, l1 int32, l2 int32) int32
	Hz_read(m *Module, l0 int32, l1 int32, l2 int32) int32
}
type Module struct {
	memory      []byte
	maxMem      uint64
	M           unsafe.Pointer
	g0          int32
	hanzo       HanzoImports
	memMu       *sync.Mutex
	memSize     *atomic.Uint64
	dataEnd     uint32
	memShared   bool
	threads     *threadPool
	threadStart func(*Module, int32, int32)
}

func New(hanzo HanzoImports) *Module {
	m := &Module{hanzo: hanzo}
	m.memory = make([]byte, 2293760, 2867200)
	m.memMu = &sync.Mutex{}
	m.memSize = &atomic.Uint64{}
	m.threads = &threadPool{}
	m.memSize.Store(2293760)
	m.M = unsafe.Pointer(unsafe.SliceData(m.memory))
	m.maxMem = 4294967296
	m.g0 = int32(1048576)
	m.dataEnd = 1048916
	initData_0(m)
	return m
}

const InitialMemoryBytes = 2293760

func NewWithMemory(hanzo HanzoImports, memory []byte, memSize uint64) *Module {
	m := &Module{hanzo: hanzo}
	m.memory = memory
	m.memMu = &sync.Mutex{}
	m.memSize = &atomic.Uint64{}
	m.threads = &threadPool{}
	if memSize > 4294836224 {
		panic("wasm2go: memory size exceeds the implementation limit (4294836224 bytes)")
	}
	m.memSize.Store(memSize)
	m.M = unsafe.Pointer(unsafe.SliceData(m.memory))
	m.maxMem = uint64(len(memory))
	m.g0 = int32(1048576)
	m.dataEnd = 1048916
	return m
}
func NewFromSnapshot(hanzo HanzoImports, memory []byte, memSize uint64, globals []uint64) *Module {
	m := &Module{hanzo: hanzo}
	m.memory = memory
	m.memMu = &sync.Mutex{}
	m.memSize = &atomic.Uint64{}
	m.threads = &threadPool{}
	if memSize > 4294836224 {
		panic("wasm2go: memory size exceeds the implementation limit (4294836224 bytes)")
	}
	m.memSize.Store(memSize)
	m.M = unsafe.Pointer(unsafe.SliceData(m.memory))
	m.maxMem = uint64(len(memory))
	m.g0 = int32(1048576)
	m.dataEnd = 1048916
	restoreGlobals(m, globals)
	return m
}
func initData_0(m *Module) {
	copy(m.memory[1048576:], wasm2goData_data_bin[0:340])
}

var _consts = [12]uintptr{2097748, 2097752, 2246488, 2098268, 2098280, 2102396, 2098288, 2098284, 2093104, 2102392, 16604, 2102388}

func (m *Module) HzAlloc(l0 int32) int32 {
	return fn17(m, l0)
}
func (m *Module) HzBegin(l0 int32, l1 int32, l2 int32, l3 int32) int32 {
	return fn18(m, l0, l1, l2, l3)
}
func (m *Module) HzCancel(l0 int32) {
	fn19(m, l0)
}
func (m *Module) HzFree(l0 int32, l1 int32) {
	fn20(m, l0, l1)
}
func (m *Module) HzInit(l0 int32, l1 int32) int32 {
	return fn21(m, l0, l1)
}
func (m *Module) HzPoll(l0 int64) int64 {
	return fn22(m, l0)
}
func (m *Module) Memory() []byte {
	return m.memory
}

// ui32 / ui64 reinterpret a signed integer as its unsigned bit
// equivalent at runtime. Used for the operands of wasm unsigned
// comparisons (i32.lt_u etc.) — emitting `uint32(int32(-N))` directly
// fails Go's compile-time constant rule because the negative typed
// constant isn't representable in uint32; routing through these
// function-call boundaries forces runtime conversion.
func ui32(x int32) uint32 { return uint32(x) }

// b2i32 materialises a wasm comparison result — an i32 that is 0 or 1 — from
// the Go bool the comparison expression evaluates to.
//
// It exists as a named helper rather than an inline `func() int32 { ... }()`
// because the gcasm backend requires every direct call left in the compiled
// output to be either a package-local FnN or something the Go inliner removed.
// A func literal is normally inlined at its call site, but the inliner gives up
// once the ENCLOSING function grows past its budget — and a single wasm function
// can translate to tens of thousands of lines of Go, as an interpreter's
// bytecode dispatch loop does. The literal is then outlined into a real closure
// symbol (FnN.funcA.funcB), which reaches the assembler as a direct call gcasm
// cannot marshal. A named helper this small is always inlined, and if it ever
// were not, it would fail loudly at its own symbol rather than as a nested
// closure.
func b2i32(b bool) int32 {
	if b {
		return 1
	}
	return 0
}

//go:noinline
func wasm_trap_div_zero() { panic("wasm: integer divide by zero") }

//go:noinline
func wasm_trap_int_overflow() { panic("wasm: integer overflow") }

//go:noinline
func wasm_trap_invalid_conv() { panic("wasm: invalid conversion to integer") }

//go:noinline
func wasm_trap_unreachable() { panic("wasm: unreachable") }

//go:noinline
func wasm_trap_memfill_oob() { panic("wasm: memory.fill out of bounds") }

//go:noinline
func wasm_trap_memcopy_oob() { panic("wasm: memory.copy out of bounds") }

// accessMemory runs f with the module's current linear memory while
// holding the same lock memoryGrow takes to mutate the memory slice
// header or relocate its backing array. It is the ONE safe way to
// touch linear memory from OUTSIDE the module's execution goroutine —
// e.g. a watchdog goroutine raising CPython's eval-breaker bit while
// an evaluation is running. For the duration of f the memory can
// neither be resliced nor relocated, so f's writes land in the array
// the guest observes; a grow that raced in just before blocks until f
// returns and then copies f's writes forward with the rest of the
// contents. Determinism notes for callers:
//
//   - f MUST NOT call back into the module or into memoryGrow — that
//     would self-deadlock.
//   - f should be short: a running guest blocks inside memory.grow
//     until f returns (ordinary guest loads/stores do not block).
//   - Bytes the guest reads or writes concurrently with f (that is
//     the point of an eval-breaker-style flag) are exchanged with
//     plain single-word accesses; keep such shared words
//     word-aligned and word-sized.
func accessMemory(m *Module, f func(mem []byte)) {
	m.memMu.Lock()
	defer m.memMu.Unlock()
	f(m.memory)
}

func memoryFill(m *Module, dst int32, val int32, n int32) {
	if n == 0 {
		return
	}
	end := uint64(uint32(dst)) + uint64(uint32(n))
	if end > m.memSize.Load() {
		wasm_trap_memfill_oob()
	}
	b := m.memory[uint32(dst):uint32(end)]
	v := byte(val)

	if v == 0 {
		for k := range b {
			b[k] = 0
		}
		return
	}
	b[0] = v
	for filled := 1; filled < len(b); filled *= 2 {
		copy(b[filled:], b[:filled])
	}
}

func memoryCopy(m *Module, dst int32, src int32, n int32) {
	if n == 0 {
		return
	}
	srcEnd := uint64(uint32(src)) + uint64(uint32(n))
	dstEnd := uint64(uint32(dst)) + uint64(uint32(n))
	if size := m.memSize.Load(); srcEnd > size || dstEnd > size {
		wasm_trap_memcopy_oob()
	}
	copy(m.memory[uint32(dst):uint32(dstEnd)], m.memory[uint32(src):uint32(srcEnd)])
}

var spinAgents int32
var spinOversubscribed uint32

type threadPool struct {
	nextTID atomic.Int32
	wg      sync.WaitGroup

	parkMu sync.Mutex
	parked map[uint64][]chan struct{}
}

// wake releases up to count waiters on ea and reports how many it woke.
func (p *threadPool) wake(ea uint64, count int32) int32 {
	p.parkMu.Lock()
	defer p.parkMu.Unlock()
	waiters := p.parked[ea]
	n := int32(len(waiters))
	if count >= 0 && count < n {
		n = count
	}
	for _, ch := range waiters[:n] {
		close(ch)
	}
	if int(n) == len(waiters) {
		delete(p.parked, ea)
	} else {
		p.parked[ea] = waiters[n:]
	}
	return n
}

// saveGlobals returns the module's mutable globals, in a form that can be handed back
// to restoreGlobals. It is how a snapshot of an instance captures the state that does not
// live in linear memory.
func saveGlobals(m *Module) []uint64 {
	g := make([]uint64, 1)
	g[0] = uint64(uint32(m.g0))
	return g
}

// restoreGlobals puts a snapshot's globals back. A snapshot from a different module (or a
// different build of the same one) has a different global count; rather than
// index out of bounds, take what fits and leave the rest at their declared
// initializers.
func restoreGlobals(m *Module, g []uint64) {
	if len(g) != 1 {
		return
	}
	m.g0 = int32(uint32(g[0]))
}

//go:embed data.bin
var wasm2goData_data_bin []byte
