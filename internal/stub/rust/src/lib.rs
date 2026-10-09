//! A plugin that relays every request to the upstream its configuration names,
//! as a family service would: the request goes up under the key STUB_KEY, the
//! answer comes back with the upstream's status, headers and body, the arm and
//! provider it names, and what it cost. It is the host's test of its own ABI,
//! compiled the way a real plugin is (wasm32-wasip1, then wasm2go). Several calls
//! run at once, each a state machine stepped by hz_poll.
#![no_std]

use core::ptr::{addr_of, addr_of_mut};

#[panic_handler]
fn panic(_: &core::panic::PanicInfo) -> ! {
    core::arch::wasm32::unreachable()
}

#[link(wasm_import_module = "hanzo")]
extern "C" {
    fn hz_open(call: i32, meta: *const u8, meta_len: i32, body: *const u8, body_len: i32) -> i32;
    fn hz_head(h: i32, buf: *mut u8, cap: i32) -> i32;
    fn hz_read(h: i32, buf: *mut u8, cap: i32) -> i32;
    fn hz_close(h: i32);
    fn hz_reply(call: i32, meta: *const u8, len: i32) -> i32;
    fn hz_write(call: i32, buf: *const u8, len: i32) -> i32;
    fn hz_end(call: i32, meta: *const u8, len: i32) -> i32;
}

const PENDING: i32 = -1;
const GONE: i32 = -3;

// A bump arena: hz_alloc hands out its bytes and hz_free forgets them. The arena
// starts again whenever no call holds anything.
const ARENA: usize = 1 << 20;
static mut HEAP: [u8; ARENA] = [0; ARENA];
static mut TOP: usize = 0;

#[no_mangle]
pub extern "C" fn hz_alloc(n: i32) -> *mut u8 {
    unsafe {
        let at = (TOP + 7) & !7;
        if at + n as usize > ARENA {
            core::arch::wasm32::unreachable()
        }
        TOP = at + n as usize;
        addr_of_mut!(HEAP).cast::<u8>().add(at)
    }
}

#[no_mangle]
pub extern "C" fn hz_free(_: *mut u8, _: i32) {}

static mut BASE: [u8; 256] = [0; 256];
static mut BASE_LEN: usize = 0;

#[no_mangle]
pub extern "C" fn hz_init(cfg: *const u8, len: i32) -> i32 {
    if len < 0 || len as usize > 256 {
        return -4;
    }
    unsafe {
        core::ptr::copy_nonoverlapping(cfg, addr_of_mut!(BASE).cast::<u8>(), len as usize);
        BASE_LEN = len as usize;
    }
    0
}

#[derive(Clone, Copy, PartialEq)]
enum Step {
    Free,
    Open,
    Head,
    Body,
    End,
}

#[derive(Clone, Copy)]
struct Call {
    step: Step,
    up: i32,
    url: [u8; 512],
    url_len: usize,
    method: [u8; 8],
    method_len: usize,
    body: *const u8,
    body_len: usize,
    chunk: [u8; 4096],
    chunk_len: usize,
    chunk_off: usize,
}

const NONE: Call = Call {
    step: Step::Free,
    up: 0,
    url: [0; 512],
    url_len: 0,
    method: [0; 8],
    method_len: 0,
    body: core::ptr::null(),
    body_len: 0,
    chunk: [0; 4096],
    chunk_len: 0,
    chunk_off: 0,
};

const SLOTS: usize = 32;
static mut CALLS: [Call; SLOTS] = [NONE; SLOTS];

fn find(hay: &[u8], needle: &[u8]) -> Option<usize> {
    if needle.len() > hay.len() {
        return None;
    }
    'at: for i in 0..=hay.len() - needle.len() {
        for j in 0..needle.len() {
            if hay[i + j] != needle[j] {
                continue 'at;
            }
        }
        return Some(i);
    }
    None
}

// field reads the string value of "key" from a flat JSON object.
fn field<'a>(json: &'a [u8], key: &[u8]) -> &'a [u8] {
    let mut pat = [0u8; 32];
    let n = key.len() + 4;
    pat[0] = b'"';
    pat[1..1 + key.len()].copy_from_slice(key);
    pat[1 + key.len()..n].copy_from_slice(b"\":\"");
    match find(json, &pat[..n]) {
        Some(i) => {
            let rest = &json[i + n..];
            let end = rest.iter().position(|&c| c == b'"').unwrap_or(0);
            &rest[..end]
        }
        None => &[],
    }
}

#[no_mangle]
pub extern "C" fn hz_begin(ask: *const u8, ask_len: i32, body: *const u8, body_len: i32) -> i32 {
    unsafe {
        let ask = core::slice::from_raw_parts(ask, ask_len as usize);
        let calls = &mut *addr_of_mut!(CALLS);
        let Some(i) = calls.iter().position(|c| c.step == Step::Free) else {
            return -4;
        };
        let c = &mut calls[i];
        *c = NONE;
        let base = core::slice::from_raw_parts(addr_of!(BASE).cast::<u8>(), BASE_LEN);
        let path = field(ask, b"path");
        if base.len() + path.len() > 512 {
            return -4;
        }
        c.url[..base.len()].copy_from_slice(base);
        c.url[base.len()..base.len() + path.len()].copy_from_slice(path);
        c.url_len = base.len() + path.len();
        let method = field(ask, b"method");
        c.method[..method.len().min(8)].copy_from_slice(&method[..method.len().min(8)]);
        c.method_len = method.len().min(8);
        // The host's buffers are its own past this call: keep a copy.
        let keep = hz_alloc(body_len);
        core::ptr::copy_nonoverlapping(body, keep, body_len as usize);
        c.body = keep;
        c.body_len = body_len as usize;
        c.step = Step::Open;
        (i + 1) as i32
    }
}

#[no_mangle]
pub extern "C" fn hz_cancel(call: i32) {
    unsafe {
        let calls = &mut *addr_of_mut!(CALLS);
        if call < 1 || call as usize > SLOTS {
            return;
        }
        let c = &mut calls[call as usize - 1];
        if c.up > 0 {
            hz_close(c.up);
        }
        c.step = Step::Free;
        settle();
    }
}

// settle starts the arena again once no call is live.
fn settle() {
    unsafe {
        let calls = &*addr_of!(CALLS);
        if calls.iter().all(|c| c.step == Step::Free) {
            TOP = 0;
        }
    }
}

fn push(out: &mut [u8], at: &mut usize, b: &[u8]) {
    out[*at..*at + b.len()].copy_from_slice(b);
    *at += b.len();
}

// step moves one call as far as it can go, and reports whether it moved.
fn step(id: i32, c: &mut Call) -> bool {
    unsafe {
        match c.step {
            Step::Free => false,
            Step::Open => {
                let mut meta = [0u8; 768];
                let mut n = 0;
                push(&mut meta, &mut n, b"{\"method\":\"");
                push(&mut meta, &mut n, &c.method[..c.method_len]);
                push(&mut meta, &mut n, b"\",\"url\":\"");
                push(&mut meta, &mut n, &c.url[..c.url_len]);
                push(&mut meta, &mut n, b"\",\"key\":\"STUB_KEY\",\"headers\":[[\"content-type\",\"application/json\"]]}");
                let h = hz_open(id, meta.as_ptr(), n as i32, c.body, c.body_len as i32);
                if h < 0 {
                    let r = b"{\"status\":502,\"headers\":[[\"content-type\",\"application/json\"]]}";
                    hz_reply(id, r.as_ptr(), r.len() as i32);
                    let e = b"{\"error\":{\"message\":\"stub: no upstream\"}}";
                    hz_write(id, e.as_ptr(), e.len() as i32);
                    hz_end(id, b"{}".as_ptr(), 2);
                    c.step = Step::Free;
                    return true;
                }
                c.up = h;
                c.step = Step::Head;
                true
            }
            Step::Head => {
                let mut head = [0u8; 8192];
                let n = hz_head(c.up, head.as_mut_ptr(), head.len() as i32);
                if n == PENDING || n as usize > head.len() || n < 2 {
                    return false;
                }
                // The upstream's head, with the arm and provider that ran it.
                let mut reply = [0u8; 8400];
                let mut at = 0;
                push(&mut reply, &mut at, b"{\"arm\":\"stub-arm\",\"provider\":\"stub\",\"trailers\":[\"X-Hanzo-Cogs\"],");
                push(&mut reply, &mut at, &head[1..n as usize]);
                if hz_reply(id, reply.as_ptr(), at as i32) == GONE {
                    hz_close(c.up);
                    c.step = Step::Free;
                    return true;
                }
                c.step = Step::Body;
                true
            }
            Step::Body => {
                let mut moved = false;
                loop {
                    if c.chunk_off < c.chunk_len {
                        let w = hz_write(id, c.chunk.as_ptr().add(c.chunk_off), (c.chunk_len - c.chunk_off) as i32);
                        if w == PENDING {
                            return moved;
                        }
                        if w < 0 {
                            hz_close(c.up);
                            c.step = Step::Free;
                            return true;
                        }
                        c.chunk_off += w as usize;
                        moved = true;
                        continue;
                    }
                    let r = hz_read(c.up, c.chunk.as_mut_ptr(), c.chunk.len() as i32);
                    if r == PENDING {
                        return moved;
                    }
                    if r <= 0 {
                        hz_close(c.up);
                        c.up = 0;
                        c.step = Step::End;
                        return true;
                    }
                    c.chunk_len = r as usize;
                    c.chunk_off = 0;
                    moved = true;
                }
            }
            Step::End => {
                let e = b"{\"cogs\":\"0.000123\",\"failover\":[\"vendor/a (free): upstream status 429\"]}";
                hz_end(id, e.as_ptr(), e.len() as i32);
                c.step = Step::Free;
                true
            }
        }
    }
}

#[no_mangle]
pub extern "C" fn hz_poll(_now: i64) -> i64 {
    unsafe {
        let calls = &mut *addr_of_mut!(CALLS);
        loop {
            let mut moved = false;
            for (i, c) in calls.iter_mut().enumerate() {
                moved |= step((i + 1) as i32, c);
            }
            if !moved {
                break;
            }
        }
    }
    settle();
    -1
}
