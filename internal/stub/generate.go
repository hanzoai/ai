// Package stub is a plugin the host tests its ABI against: rust/ built for
// wasm32-wasip1, then translated to Go by wasm2go, as every plugin is. It relays
// each request to the upstream its configuration names under the key STUB_KEY,
// replies with the upstream's head and the arm and provider that ran it, streams
// the body, and ends stating what it cost and an arm that failed first.
package stub

//go:generate sh -c "CARGO_TARGET_DIR=$${TMPDIR:-/tmp}/stub cargo build --release --manifest-path rust/Cargo.toml --target wasm32-wasip1 && wasm2go -i $${TMPDIR:-/tmp}/stub/wasm32-wasip1/release/stub.wasm -o stub.go -pkg stub -import github.com/hanzoai/ai/internal/stub"
