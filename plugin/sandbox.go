// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package plugin

import (
	"bytes"
	"io"
	"strings"
	"sync"

	"github.com/hanzoai/ai/log"
)

// wasi is the WASI implementation wasm2go generates beside a plugin built for
// wasm32-wasip1, as far as the host configures it.
type wasi interface {
	SetEnv(env []string)
	SetArgs(args []string)
	SetStdin(r io.Reader)
	SetStdout(w io.Writer)
	SetStderr(w io.Writer)
	SetFSAccessHook(hook func(path string, write bool) bool)
	SetNetAccessHook(hook func(op string) bool)
	SetDialHook(hook func(network, host, ip string, port int) bool)
	SetResolveHook(hook func(host string) bool)
	SetExecHook(hook func(path string, argv []string) bool)
}

// Sandbox closes a plugin's WASI to everything but its clocks, its randomness
// and its own output. It gets no environment, because the keys live there; no
// files, no sockets and no processes, because everything it reaches it reaches
// through hz_open. What it writes is logged a line at a time under its name.
//
//	zen.NewWithWASI(plugin.Sandbox(zen.DefaultWASI(), "zen"), plugin.Imports[*zen.Module]{H: h})
func Sandbox[W wasi](w W, name string) W {
	w.SetEnv(nil)
	w.SetArgs([]string{name})
	w.SetStdin(bytes.NewReader(nil))
	out := &lines{name: name}
	w.SetStdout(out)
	w.SetStderr(out)
	w.SetFSAccessHook(func(string, bool) bool { return false })
	w.SetNetAccessHook(func(string) bool { return false })
	w.SetDialHook(func(string, string, string, int) bool { return false })
	w.SetResolveHook(func(string) bool { return false })
	w.SetExecHook(func(string, []string) bool { return false })
	return w
}

// lines logs what a plugin writes, a line at a time.
type lines struct {
	name string
	mu   sync.Mutex
	held []byte
}

func (l *lines) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.held = append(l.held, p...)
	for {
		i := bytes.IndexByte(l.held, '\n')
		if i < 0 {
			break
		}
		if s := strings.TrimSpace(string(l.held[:i])); s != "" {
			log.Info("plugin %s: %s", l.name, s)
		}
		l.held = l.held[i+1:]
	}
	return len(p), nil
}
