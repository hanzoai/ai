// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// A ZAP handler that stores a row says whose row it is.
//
// These take the row off the body, owner field and all, and the listings beside
// them are scoped — so without this a row is written where no listing would show
// it. The HTTP surface learned the same rule; this is what keeps the two from
// drifting apart again, table by table.
//
// What counts as saying so: stamping the owner (theirOrg / themselves), refusing
// an id out of reach (zapReachable), resolving the row first (storeFor,
// zapKSFVScopedOwner), or gating the whole handler on the platform admin — which
// is a different control and a sufficient one.
func TestEveryZapWriteSaysWhoseRowItIs(t *testing.T) {
	// The mechanisms that answer it. Each is a real one, not a spelling: stamp the
	// owner, refuse an id out of reach, resolve the row, gate on the platform admin,
	// check the row names the caller, or derive the scope from the principal.
	says := regexp.MustCompile(`theirOrg\(|themselves\(|zapReachable\(|storeFor\(|` +
		`zapKSFVScopedOwner\(|SuperAdmin\(|zapWrite\(|zapIsCurrentUser\(|` +
		`zapMemoryIdentity\(|zapRPSOrg\(|user\.Owner|user\.Name|sa\.Owner`)
	writes := regexp.MustCompile(`object\.(Add|Update|Delete)\w+\(`)

	// Handlers that store a row and answer for it another way. Each is here
	// because of what it is, not because nobody got to it.
	named := map[string]string{
		// Keyed by (store, key) rather than by an owner, and identical on both
		// surfaces — whether a store may be written to is its own question.
		"zapAddTreeFileHandler":    "keyed by store and key, not by an owner",
		"zapUpdateTreeFileHandler": "keyed by store and key, not by an owner",
		"zapDeleteTreeFileHandler": "keyed by store and key, not by an owner",
		// Gated on the platform admin through zapMiscSuperAdmin — a map this check
		// cannot see, because the gate is a lookup rather than a call. Naming any
		// organization's settings is what that endpoint is for.
		"zapOrgSettingsHandler": "gated on the platform admin, by a map rather than a call",
	}

	fset := token.NewFileSet()
	paths, err := filepath.Glob("zap_*.go")
	if err != nil {
		t.Fatal(err)
	}
	open, checked := []string{}, 0
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		src, err := filepath.Abs(path)
		if err != nil {
			t.Fatal(err)
		}
		_ = src
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || !strings.HasSuffix(fn.Name.Name, "Handler") {
				continue
			}
			body := source(t, fset, fn)
			if !writes.MatchString(body) {
				continue
			}
			checked++
			if _, ours := named[fn.Name.Name]; ours {
				continue
			}
			if !says.MatchString(body) {
				open = append(open, fn.Name.Name)
			}
		}
	}
	sort.Strings(open)
	for _, name := range open {
		t.Errorf("%s stores a row without saying whose it is — stamp the owner, refuse "+
			"an id out of reach, or name it in this test with the reason", name)
	}
	if checked == 0 {
		t.Fatal("found no write handlers")
	}
	t.Logf("%d write handlers checked", checked)
}

// source renders a function back to text, which is what the checks above read.
func source(t *testing.T, fset *token.FileSet, fn *ast.FuncDecl) string {
	t.Helper()
	start, end := fset.Position(fn.Pos()), fset.Position(fn.End())
	raw, err := os.ReadFile(start.Filename)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw[start.Offset:end.Offset])
}

// The same rule on the other surface.
//
// An HTTP handler that stores a row says whose it is, by one of the same
// mechanisms: resolving the caller's scope (GetScopedOwner, RequireSignedIn,
// RequireSessionOwner), refusing a row out of reach (reaches, storeFor,
// applicationFor), or gating on the platform admin. The generic writers say it
// for the eighteen handlers that are one line each.
func TestEveryHttpWriteSaysWhoseRowItIs(t *testing.T) {
	says := regexp.MustCompile(`GetScopedOwner\(|RequireSignedIn\(|RequireSessionOwner\(|` +
		`reaches\(|storeFor\(|applicationFor\(|whose\(|RequireSuperAdmin\(|` +
		`stored\(c, |replaced\(c, |listed\(c, |ownedBy\(|connectionFor\(|` +
		`IsCurrentUser\(|requireMemoryIdentity\(|GetSessionUser\(`)
	writes := regexp.MustCompile(`object\.(Add|Update|Delete)\w+\(`)

	// NOT YET ANSWERED.
	//
	// These store a row and do not say whose it is by any mechanism this check
	// knows. Some of them will turn out to be scoped another way, as thirteen of
	// the ZAP handlers did — each has to be read to find out, and reading them is
	// the work this list makes visible rather than the claim that it is done.
	//
	// The list is here so the build catches a NEW one. Removing a name is the
	// point; adding one needs a reason beside it.
	named := map[string]string{
		"account.go:Signin":                          "not yet answered",
		"account.go:addInitialChat":                  "not yet answered",
		"account.go:addInitialChatAndMessage":        "not yet answered",
		"connections_api.go:DeleteAIConnection":      "not yet answered",
		"file.go:DeleteFile":                         "not yet answered",
		"finetune.go:CancelFinetuneJob":              "not yet answered",
		"finetune.go:CreateFinetuneJob":              "not yet answered",
		"finetune.go:DeployFinetuneJob":              "not yet answered",
		"finetune.go:refreshFinetuneJob":             "not yet answered",
		"form.go:UpdateForm":                         "not yet answered",
		"graph_chat.go:generateChatGraphData":        "not yet answered",
		"message.go:DeleteMessage":                   "not yet answered",
		"message.go:DeleteWelcomeMessage":            "not yet answered",
		"message_answer.go:GetMessageAnswer":         "not yet answered",
		"node.go:AddNode":                            "not yet answered",
		"node.go:DeleteNode":                         "not yet answered",
		"node.go:UpdateNode":                         "not yet answered",
		"rag.go:RagDelete":                           "not yet answered",
		"record.go:AddRecord":                        "not yet answered",
		"record.go:AddRecords":                       "not yet answered",
		"record.go:DeleteRecord":                     "not yet answered",
		"record.go:UpdateRecord":                     "not yet answered",
		"router_stats.go:UpdateTrainingContribution": "not yet answered",
		"routing_defaults.go:DeleteMyRoutingData":    "not yet answered",
		"scale.go:AddScale":                          "not yet answered",
		"scale.go:DeleteScale":                       "not yet answered",
		"scale.go:UpdateScale":                       "not yet answered",
		"scan.go:DeleteScan":                         "not yet answered",
		"session.go:AddSession":                      "not yet answered",
		"session.go:DeleteSession":                   "not yet answered",
		"session.go:UpdateSession":                   "not yet answered",
		"task.go:AnalyzeTask":                        "not yet answered",
		"task.go:DeleteTask":                         "not yet answered",
		"task.go:UpdateTask":                         "not yet answered",
		"vector.go:AddVector":                        "not yet answered",
		"vector.go:UpdateVector":                     "not yet answered",
		"video.go:UpdateVideo":                       "not yet answered",
		"workflow.go:AddWorkflow":                    "not yet answered",
		"workflow.go:UpdateWorkflow":                 "not yet answered",
	}

	fset := token.NewFileSet()
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	open, checked := []string{}, 0
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") || strings.HasPrefix(path, "zap_") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Recv == nil {
				continue
			}
			body := source(t, fset, fn)
			if !writes.MatchString(body) {
				continue
			}
			checked++
			if _, known := named[path+":"+fn.Name.Name]; known {
				continue
			}
			if !says.MatchString(body) {
				open = append(open, path+":"+fn.Name.Name)
			}
		}
	}
	sort.Strings(open)
	for _, name := range open {
		t.Errorf("%s stores a row without saying whose it is", name)
	}
	if checked == 0 {
		t.Fatal("found no write handlers")
	}
	t.Logf("%d write handlers checked", checked)
}
