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
	if got["WebSocket"] != 1 {
		t.Errorf("WebSocket sites = %d, want 1", got["WebSocket"])
	}
	if got["WebSocket.Server"] != 2 {
		t.Errorf("WebSocket.Server sites = %d, want 2", got["WebSocket.Server"])
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
		t.Errorf("aliased import should record original name WebSocket: %v", got)
	}
	if got["WS.OPEN"] != 1 {
		t.Errorf("member access on local alias not recorded: %v", got)
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
	if got["ws.createWebSocketStream"] != 1 || got["WS.Server"] != 1 {
		t.Errorf("namespace/default member access: %v", got)
	}
	for _, sym := range s.Symbols {
		for _, site := range sym.Sites {
			if filepath.Base(filepath.Dir(site.File)) == "x" {
				t.Errorf("node_modules not skipped: %v", site)
			}
		}
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
	if got["Sender"] != 1 {
		t.Errorf("subpath require not recorded: %v", got)
	}
	if _, ok := got["other"]; ok {
		t.Errorf("prefix-only match leaked: %v", got)
	}
}

func TestUnsupportedEcosystem(t *testing.T) {
	if _, err := Index("cobol", ".", "x"); err == nil {
		t.Fatal("expected error for unregistered ecosystem")
	}
}
