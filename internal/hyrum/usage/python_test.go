package usage

import "testing"

func TestPyFromImport(t *testing.T) {
	root := writeTree(t, map[string]string{
		"core.py": "from flask import Flask, jsonify, request as rq\n" +
			"app = Flask(__name__)\n" +
			"rq.args.get('x')\n" +
			"jsonify({'a': 1})\n",
	})
	s, err := Index("pypi", root, "flask")
	if err != nil {
		t.Fatal(err)
	}
	got := symbolNames(s)
	for _, want := range []string{"Flask", "jsonify", "request", "request.args"} {
		if got[want] == 0 {
			t.Errorf("missing %q: %v", want, got)
		}
	}
}

func TestPyModuleImport(t *testing.T) {
	root := writeTree(t, map[string]string{
		"a.py": "import flask\n" +
			"import flask.json as fjson\n" +
			"flask.jsonify(x)\n" +
			"fjson.dumps(x)\n",
	})
	s, err := Index("pypi", root, "flask")
	if err != nil {
		t.Fatal(err)
	}
	got := symbolNames(s)
	if got["flask"] == 0 || got["flask.json"] == 0 {
		t.Errorf("module imports not recorded: %v", got)
	}
	if got["flask.jsonify"] == 0 {
		t.Errorf("attribute access on module binding not recorded: %v", got)
	}
	if got["fjson.dumps"] == 0 {
		t.Errorf("attribute access on aliased module not recorded: %v", got)
	}
}

func TestPyNoPrefixLeak(t *testing.T) {
	root := writeTree(t, map[string]string{
		"a.py": "import flask\n" +
			"unflask.something()\n", // 'flask.' appears mid-identifier
	})
	s, err := Index("pypi", root, "flask")
	if err != nil {
		t.Fatal(err)
	}
	got := symbolNames(s)
	if got["flask.something"] != 0 {
		t.Errorf("mid-identifier match leaked: %v", got)
	}
}

func TestPyMultilineFromImport(t *testing.T) {
	root := writeTree(t, map[string]string{
		"core.py": "from flask import (\n" +
			"    Flask,\n" +
			"    jsonify as flask_jsonify,\n" +
			"    abort,\n" +
			")\n" +
			"flask_jsonify({})\n",
	})
	s, err := Index("pypi", root, "flask")
	if err != nil {
		t.Fatal(err)
	}
	got := symbolNames(s)
	for _, want := range []string{"Flask", "jsonify", "abort"} {
		if got[want] == 0 {
			t.Errorf("multi-line import missing %q: %v", want, got)
		}
	}
	// Site should anchor at the opening line.
	for _, sym := range s.Symbols {
		if sym.Name == "Flask" && sym.Sites[0].Line != 1 {
			t.Errorf("multi-line import site line = %d, want 1", sym.Sites[0].Line)
		}
	}
}

func TestPyHyphenUnderscore(t *testing.T) {
	root := writeTree(t, map[string]string{
		"a.py": "from engine_io_parser import decode\n",
	})
	s, err := Index("pypi", root, "engine-io-parser")
	if err != nil {
		t.Fatal(err)
	}
	if symbolNames(s)["decode"] == 0 {
		t.Errorf("hyphen/underscore normalisation failed: %v", symbolNames(s))
	}
}
