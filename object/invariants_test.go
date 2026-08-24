// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package object

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Three things about this module are true, cost a process each to learn, and
// cannot be seen by reading one file. They are asserted here, over one reading of
// the source, because a reading is the only place they are visible.
//
//   - Nothing dereferences a row that is not there. The store answers a missing
//     one with (nil, nil), so absence never arrives as an error.
//   - No callback that outlives its request reaches back through the controller.
//     A streamed body and a hijacked socket both run after fiber has released the
//     request context, in a goroutine of their own.
//   - Every package map written at runtime is written under a lock. A concurrent
//     map write is a runtime fatal error, which no recover answers for.
//   - Every stored timestamp is written by util, so they all have one shape. They
//     are TEXT columns ordered as text, and a shape that varies is an order that
//     is not time.
//
// Each ends the PROCESS rather than the request, which is why none of them is a
// comment.
type source struct {
	fset  *token.FileSet
	files map[string]*ast.File
}

// read parses the packages these invariants range over, once.
func read(t *testing.T, dirs ...string) *source {
	t.Helper()
	s := &source{fset: token.NewFileSet(), files: map[string]*ast.File{}}
	for _, dir := range dirs {
		paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range paths {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			file, err := parser.ParseFile(s.fset, path, nil, parser.ParseComments)
			if err != nil {
				t.Fatalf("%s: %v", path, err)
			}
			s.files[path] = file
		}
	}
	if len(s.files) == 0 {
		t.Fatal("read no source — the invariants below would hold vacuously")
	}
	return s
}

func (s *source) at(pos token.Pos) string { return s.fset.Position(pos).String() }

// funcs yields every function and method in the read source.
func (s *source) funcs(yield func(path string, fn *ast.FuncDecl)) {
	paths := make([]string, 0, len(s.files))
	for p := range s.files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		for _, decl := range s.files[p].Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
				yield(p, fn)
			}
		}
	}
}

// A row that is not there is (nil, nil): the value and the error are nil
// together, so absence never arrives as an error and every reader has to say
// what it does about it. The ones that did not were the sharpest failures here —
// a dereference inside a hijacked connection or a stream writer is a goroutine's
// panic, which ends the process rather than the request.
func TestNoAbsentRowIsDereferenced(t *testing.T) {
	s := read(t, ".", "../controllers", "../cluster")

	// Which functions answer (nil, nil), and which names more than one declaration
	// carries. A shared name cannot be resolved by reading the syntax, so it is not
	// checked, and the set of them is asserted below.
	absent := map[string]bool{}
	declarations := map[string]int{}
	s.funcs(func(_ string, fn *ast.FuncDecl) {
		// A method is not the package function of the same name. object.GetVideo(id)
		// answers a row and an error; (c *ApiController) GetVideo() answers a request
		// and takes nothing, so it never appears as the right-hand side of the
		// two-value assignment this looks for. Counting them as one name made every
		// store read whose handler mirrors it unresolvable, which is most of them.
		if fn.Recv != nil {
			return
		}
		declarations[fn.Name.Name]++
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			ret, ok := n.(*ast.ReturnStmt)
			if !ok || len(ret.Results) != 2 {
				return true
			}
			a, aok := ret.Results[0].(*ast.Ident)
			b, bok := ret.Results[1].(*ast.Ident)
			if aok && bok && a.Name == "nil" && b.Name == "nil" {
				absent[fn.Name.Name] = true
			}
			return true
		})
	})

	// A function that answers with another's answer carries that answer's absence:
	//   func getVideo(o, n string) (*Video, error) { return getRow[Video](...) }
	// The literal (nil, nil) is two hops from GetVideo, but nil is what GetVideo's
	// callers hold, so follow the hops to a fixpoint.
	through := map[string][]string{}
	s.funcs(func(_ string, fn *ast.FuncDecl) {
		// Methods are left out here for the same reason they are left out above: a
		// call names only the selector, so marking a method's name would answer for
		// every package function that shares it. (p *Provider) GetModelProvider is a
		// method and there is no function by that name, so provider.GetModelProvider
		// must not inherit an absence from anywhere.
		if fn.Recv != nil {
			return
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			ret, ok := n.(*ast.ReturnStmt)
			if !ok || len(ret.Results) != 1 {
				return true
			}
			if call, ok := ret.Results[0].(*ast.CallExpr); ok {
				through[fn.Name.Name] = append(through[fn.Name.Name], called(call.Fun))
			}
			return true
		})
	})
	for moved := true; moved; {
		moved = false
		for name, calls := range through {
			if absent[name] {
				continue
			}
			for _, c := range calls {
				if absent[c] {
					absent[name], moved = true, true
					break
				}
			}
		}
	}

	shared := []string{}
	for name := range absent {
		if declarations[name] > 1 {
			shared = append(shared, name)
		}
	}
	sort.Strings(shared)
	// Nothing is ambiguous now that a method and a package function are told apart,
	// so nothing is skipped. A name that lands here later is two package functions
	// genuinely sharing one name, which is worth seeing.
	known := []string{}
	if strings.Join(shared, ",") != strings.Join(known, ",") {
		t.Errorf("the set of names two declarations share has changed:\n  now: %v\n  was: %v\n"+
			"a shared name cannot be told apart by reading the syntax, so it is skipped — "+
			"give the new one a name of its own, or add it here deliberately", shared, known)
	}
	for _, name := range known {
		delete(absent, name)
	}

	s.funcs(func(path string, fn *ast.FuncDecl) {
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			body, ok := n.(*ast.BlockStmt)
			if !ok {
				return true
			}
			for i, stmt := range body.List {
				as, ok := stmt.(*ast.AssignStmt)
				if !ok || len(as.Lhs) != 2 || len(as.Rhs) != 1 {
					continue
				}
				call, ok := as.Rhs[0].(*ast.CallExpr)
				if !ok || !absent[called(call.Fun)] {
					continue
				}
				val, ok := as.Lhs[0].(*ast.Ident)
				if !ok || val.Name == "_" {
					continue
				}
				if where := readBeforeChecked(body.List[i+1:], val.Name); where != token.NoPos {
					t.Errorf("%s: %s answers (nil, nil) for a row that is not there, and %s is read "+
						"at %s with nothing between saying what happens when it is absent",
						path, called(call.Fun), val.Name, s.at(where))
				}
			}
			return true
		})
	})
}

// A streamed body is drained by fasthttp from its own goroutine, and a hijacked
// socket is served from another, both after the handler has returned and fiber
// has released the request context. Reading through the controller there
// dereferences nothing, and the panic is in a goroutine the router never sees.
//
// The inline form only: a function passed by NAME — anthropic's run — serves the
// streamed and un-streamed cases from one body, so a controller call in it may be
// correct, and which branch it sits in is not something a walk of the syntax can
// settle.
func TestNoCallbackOutlivingItsRequestReachesThroughTheController(t *testing.T) {
	s := read(t, "../controllers")
	checked := 0
	s.funcs(func(path string, fn *ast.FuncDecl) {
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			recv, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			var body *ast.BlockStmt
			switch {
			case sel.Sel.Name == "SendStreamWriter" && len(call.Args) == 1:
				if lit, ok := call.Args[0].(*ast.FuncLit); ok {
					body = lit.Body
				}
			case sel.Sel.Name == "Upgrade" && len(call.Args) == 2:
				if lit, ok := call.Args[1].(*ast.FuncLit); ok {
					body, recv = lit.Body, ast.NewIdent("c")
				}
			}
			if body == nil {
				return true
			}
			checked++
			ast.Inspect(body, func(m ast.Node) bool {
				inner, ok := m.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if id, ok := inner.X.(*ast.Ident); ok && id.Name == recv.Name {
					t.Errorf("%s: this callback uses %s.%s, and the request context is released by "+
						"the time it runs — read it before handing the closure over",
						s.at(inner.Pos()), recv.Name, inner.Sel.Name)
				}
				return true
			})
			return true
		})
	})
	if checked == 0 {
		t.Fatal("walked no callbacks")
	}
	t.Logf("%d callbacks checked", checked)
}

// A concurrent map write is a Go runtime fatal error, not a panic: recover() does
// not see it and the process ends. Two of the three process-enders found in this
// module were exactly that.
//
// Only maps something WRITES at runtime are in scope — the lookup tables here,
// stop words and centroids and route registries, are read by everyone and written
// by nobody. It checks that a lock is taken, not that it is the right one; the
// failure it exists for is the absence of any.
func TestEveryMapWrittenAtRuntimeIsGuarded(t *testing.T) {
	for _, dir := range []string{".", "../controllers", "../i18n", "../model", "../util"} {
		s := read(t, dir)
		maps := map[string]bool{}
		for _, file := range s.files {
			for _, name := range packageMaps(file) {
				maps[name] = true
			}
		}
		s.funcs(func(_ string, fn *ast.FuncDecl) {
			written := writes(fn.Body, maps)
			if len(written) == 0 || locks(fn.Body) || callerHoldsIt(fn) {
				return
			}
			for _, name := range written {
				t.Errorf("%s: %s writes the package map %s and takes no lock — a concurrent map "+
					"write is a runtime fatal error, which no recover answers for",
					s.at(fn.Pos()), fn.Name.Name, name)
			}
		})
	}
}

// called names the function a call expression calls.
func called(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.SelectorExpr:
		return f.Sel.Name
	case *ast.Ident:
		return f.Name
	// A generic call names its function under the type it was instantiated with:
	// getRow[Video](...) is an IndexExpr wrapping the identifier. Every store read
	// in this module is one of those, so not unwrapping them hid all of them.
	case *ast.IndexExpr:
		return called(f.X)
	case *ast.IndexListExpr:
		return called(f.X)
	}
	return ""
}

// readBeforeChecked reports where name is first read as a value, if that happens
// before anything compares it to nil.
func readBeforeChecked(rest []ast.Stmt, name string) token.Pos {
	for _, stmt := range rest {
		checked, where := false, token.NoPos
		ast.Inspect(stmt, func(n ast.Node) bool {
			if bin, ok := n.(*ast.BinaryExpr); ok {
				if x, ok := bin.X.(*ast.Ident); ok && x.Name == name {
					if y, ok := bin.Y.(*ast.Ident); ok && y.Name == "nil" {
						checked = true
					}
				}
			}
			if sel, ok := n.(*ast.SelectorExpr); ok && where == token.NoPos {
				if x, ok := sel.X.(*ast.Ident); ok && x.Name == name {
					where = sel.Pos()
				}
			}
			return true
		})
		if checked {
			return token.NoPos
		}
		if where != token.NoPos {
			return where
		}
	}
	return token.NoPos
}

// packageMaps names the package-level map variables a file declares.
func packageMaps(file *ast.File) []string {
	names := []string{}
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || !isMap(vs) {
				continue
			}
			for _, n := range vs.Names {
				if n.Name != "_" {
					names = append(names, n.Name)
				}
			}
		}
	}
	return names
}

func isMap(vs *ast.ValueSpec) bool {
	if _, ok := vs.Type.(*ast.MapType); ok {
		return true
	}
	for _, v := range vs.Values {
		switch e := v.(type) {
		case *ast.CompositeLit:
			if _, ok := e.Type.(*ast.MapType); ok {
				return true
			}
		case *ast.CallExpr:
			if id, ok := e.Fun.(*ast.Ident); ok && id.Name == "make" && len(e.Args) > 0 {
				if _, ok := e.Args[0].(*ast.MapType); ok {
					return true
				}
			}
		}
	}
	return false
}

// writes names which of maps a body assigns to or deletes from.
func writes(body *ast.BlockStmt, maps map[string]bool) []string {
	seen := map[string]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		note := func(name string) {
			if maps[name] {
				seen[name] = true
			}
		}
		switch s := n.(type) {
		case *ast.AssignStmt:
			for _, lhs := range s.Lhs {
				if idx, ok := lhs.(*ast.IndexExpr); ok {
					if id, ok := idx.X.(*ast.Ident); ok {
						note(id.Name)
					}
				}
			}
		case *ast.CallExpr:
			if id, ok := s.Fun.(*ast.Ident); ok && id.Name == "delete" && len(s.Args) > 0 {
				if m, ok := s.Args[0].(*ast.Ident); ok {
					note(m.Name)
				}
			}
		}
		return true
	})
	out := []string{}
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func locks(body *ast.BlockStmt) bool {
	held := false
	ast.Inspect(body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if sel.Sel.Name == "Lock" || sel.Sel.Name == "RLock" {
					held = true
				}
			}
		}
		return true
	})
	return held
}

// callerHoldsIt reports whether a function documents that its caller holds the
// lock. Some must — a sweep that is part of admitting decides and writes under
// one lock the caller spans, and taking it again would deadlock. The precondition
// has to be WRITTEN for this to pass: an invisible one is the same as none.
func callerHoldsIt(fn *ast.FuncDecl) bool {
	if fn.Doc == nil {
		return false
	}
	doc := strings.ToLower(fn.Doc.Text())
	return strings.Contains(doc, "caller holds") || strings.Contains(doc, "caller must hold")
}

// A stored timestamp is written by util and nowhere else.
//
// These are TEXT columns and the store orders them as text, so the SHAPE decides
// the order — the offset it carries, and how many places the fraction has. One
// writer is what keeps that shape the same for every row, and it is also where
// the two rules live: UTC, and a fixed three places. A time formatted at the call
// site is a row that sorts against the others by accident.
func TestEveryStoredTimestampIsWrittenByUtil(t *testing.T) {
	s := read(t, ".", "../controllers", "../scan", "../storage", "../model", "../cluster")
	s.funcs(func(path string, fn *ast.FuncDecl) {
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Format" || len(call.Args) != 1 {
				return true
			}
			// time.Now()... .Format(<a stored timestamp's layout>).
			//
			// The layout is what makes it one of these columns. A time rendered any
			// other way is a different kind of string — a version label is minute
			// granularity and nothing orders it — so only the two shapes these
			// columns hold are in scope.
			if !madeFromNow(sel.X) || !storedLayout(call.Args[0]) {
				return true
			}
			t.Errorf("%s: %s formats a time where it is used; a stored timestamp is "+
				"written by util so every row has one shape, and these columns are "+
				"ordered as text", s.at(call.Pos()), fn.Name.Name)
			return true
		})
	})
}

// storedLayout reports whether a layout is one a stored timestamp is written in:
// time.RFC3339, or the millisecond form beside it.
func storedLayout(arg ast.Expr) bool {
	switch a := arg.(type) {
	case *ast.SelectorExpr:
		if id, ok := a.X.(*ast.Ident); ok && id.Name == "time" {
			return a.Sel.Name == "RFC3339"
		}
	case *ast.BasicLit:
		return strings.Contains(a.Value, "15:04:05")
	}
	return false
}

// madeFromNow reports whether an expression is rooted in time.Now().
func madeFromNow(e ast.Expr) bool {
	for {
		switch x := e.(type) {
		case *ast.CallExpr:
			if sel, ok := x.Fun.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == "time" && sel.Sel.Name == "Now" {
					return true
				}
				e = sel.X
				continue
			}
			return false
		case *ast.SelectorExpr:
			e = x.X
		default:
			return false
		}
	}
}

// A db tag names a column and nothing else.
//
// xorm's tag said what a field was STORED AS — "json varchar(1000)" — and dbx
// reads the same tag as the column's NAME. One survived the migration, so a
// column was created called that, and every statement naming it failed to parse:
// no connection this module tried to create was ever stored.
func TestADbTagNamesAColumn(t *testing.T) {
	s := read(t, ".")
	bad := 0
	for path, file := range s.files {
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok {
					continue
				}
				for _, field := range st.Fields.List {
					if field.Tag == nil {
						continue
					}
					tag := field.Tag.Value
					m := regexp.MustCompile(`db:"([^"]*)"`).FindStringSubmatch(tag)
					if m == nil {
						continue
					}
					// "pk" is this module's own marker; anything else is a name, and a
					// name has no spaces or parentheses in it.
					if v := m[1]; v != "pk" && strings.ContainsAny(v, " (") {
						bad++
						t.Errorf("%s: %s.%s has db:%q — a db tag is a column NAME, and that "+
							"is a storage type from the ORM this one replaced",
							path, ts.Name.Name, field.Names[0].Name, v)
					}
				}
			}
		}
	}
	_ = bad
}
