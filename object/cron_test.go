// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package object

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A background job that panics must end that run and nothing else. The library
// runs each in a goroutine of its own and recovers nothing unless asked, so
// without the wrapper this test takes the test binary down with it — which is
// exactly what it does to the service.
func TestAPanickingJobDoesNotEndEverything(t *testing.T) {
	var ran atomic.Int32
	c := newCron()
	if _, err := c.AddFunc("@every 1s", func() {
		ran.Add(1)
		panic("a row that was not there")
	}); err != nil {
		t.Fatal(err)
	}
	c.Start()
	defer c.Stop()

	deadline := time.After(5 * time.Second)
	for ran.Load() < 2 {
		select {
		case <-deadline:
			t.Fatalf("the job ran %d times in 5s; it should have run and recovered twice", ran.Load())
		case <-time.After(50 * time.Millisecond):
		}
	}
	// Reaching here at all is the assertion: the first panic did not take the
	// process with it, and the schedule kept its next appointment.
}

// And there is one way to build a scheduler, because the recovery is not the
// library's default — a bare cron.New() is a job whose panic ends the service.
func TestEverySchedulerIsBuiltTheOneWay(t *testing.T) {
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") || path == "cron.go" {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "New" {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "cron" {
				t.Errorf("%s: cron.New() builds a scheduler that recovers nothing, so a "+
					"job's panic ends the process — use newCron()", fset.Position(call.Pos()))
			}
			return true
		})
	}
}
