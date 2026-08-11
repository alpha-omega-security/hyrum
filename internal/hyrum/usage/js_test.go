package usage

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func symbolNames(s *Surface) map[string]int {
	out := map[string]int{}
	for _, sym := range s.Symbols {
		out[sym.Name] = len(sym.Sites)
	}
	return out
}

func TestJSRequireCJS(t *testing.T) {
	root := writeTree(t, map[string]string{
		"a.js": "const WebSocket = require('ws');\n" +
			"const wss = new WebSocket.Server({ port: 0 });\n" +
			"WebSocket.Server.prototype.shouldHandle;\n",
	})
	s, err := Index("npm", root, "ws")
	if err != nil {
		t.Fatal(err)
	}
	got := symbolNames(s)
	if got["ws"] != 1 {
		t.Errorf("module import site count = %d, want 1: %v", got["ws"], got)
	}
	if got["ws.Server"] != 2 {
		t.Errorf("ws.Server sites = %d, want 2: %v", got["ws.Server"], got)
	}
}

func TestJSImportNamed(t *testing.T) {
	root := writeTree(t, map[string]string{
		"a.mjs": "import { Server, WebSocket as WS } from 'ws';\n" +
			"new Server();\n" +
			"WS.OPEN;\n",
	})
	s, err := Index("npm", root, "ws")
	if err != nil {
		t.Fatal(err)
	}
	got := symbolNames(s)
	if _, ok := got["Server"]; !ok {
		t.Errorf("named import Server not recorded: %v", got)
	}
	if _, ok := got["WebSocket"]; !ok {
		t.Errorf("aliased import should record export name WebSocket: %v", got)
	}
	if got["WebSocket.OPEN"] != 1 {
		t.Errorf("member access should record under export name: %v", got)
	}
	if _, ok := got["WS.OPEN"]; ok {
		t.Errorf("member access recorded under local alias, want export name: %v", got)
	}
}

func TestJSImportDefaultAndNamespace(t *testing.T) {
	root := writeTree(t, map[string]string{
		"a.ts": "import ws from 'ws';\n" +
			"import * as WS from 'ws';\n" +
			"ws.createWebSocketStream;\n" +
			"WS.Server;\n",
		"node_modules/x/index.js": "require('ws');\n", // must be skipped
	})
	s, err := Index("npm", root, "ws")
	if err != nil {
		t.Fatal(err)
	}
	got := symbolNames(s)
	if got["ws.createWebSocketStream"] != 1 || got["ws.Server"] != 1 {
		t.Errorf("namespace/default member access mapped through to module: %v", got)
	}
	for _, sym := range s.Symbols {
		for _, site := range sym.Sites {
			if filepath.Base(filepath.Dir(site.File)) == "x" {
				t.Errorf("node_modules not skipped: %v", site)
			}
		}
	}
}

func TestJSRequireChainedMember(t *testing.T) {
	root := writeTree(t, map[string]string{
		"a.js": "const DEFAULT_WS_ENGINE = require(\"ws\").Server;\n" +
			"new DEFAULT_WS_ENGINE({ noServer: true });\n",
	})
	s, err := Index("npm", root, "ws")
	if err != nil {
		t.Fatal(err)
	}
	got := symbolNames(s)
	if got["Server"] == 0 {
		t.Errorf("chained .Server not recorded as named export: %v", got)
	}
	if got["ws"] != 0 {
		t.Errorf("chained require should not also record the module: %v", got)
	}
}

func TestJSSubpathMatch(t *testing.T) {
	root := writeTree(t, map[string]string{
		"a.js": "const Sender = require('ws/lib/sender');\n",
		"b.js": "const other = require('wss');\n", // must not match
	})
	s, err := Index("npm", root, "ws")
	if err != nil {
		t.Fatal(err)
	}
	got := symbolNames(s)
	if got["ws/lib/sender"] != 1 {
		t.Errorf("subpath require not recorded under module path: %v", got)
	}
	if _, ok := got["wss"]; ok {
		t.Errorf("prefix-only match leaked: %v", got)
	}
}

func TestUnsupportedEcosystem(t *testing.T) {
	if _, err := Index("cobol", ".", "x"); err == nil {
		t.Fatal("expected error for unregistered ecosystem")
	}
}
