package usage

import "testing"

func TestPyFromImport(t *testing.T) {
	root := writeTree(t, map[string]string{
		"core.py": "from flask import Flask, jsonify, request as rq\n" +
			"app = Flask(__name__)\n" +
			"rq.args.get('x')\n" +
			"jsonify({'a': 1})\n",
	})
	s, err := Index(t.Context(), root, "pkg:pypi/flask")
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
	s, err := Index(t.Context(), root, "pkg:pypi/flask")
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
	if got["flask.json.dumps"] == 0 {
		t.Errorf("attribute access on aliased module should record under module path: %v", got)
	}
}

func TestPyNoPrefixLeak(t *testing.T) {
	root := writeTree(t, map[string]string{
		"a.py": "import flask\n" +
			"unflask.something()\n" + // 'flask' inside a longer identifier
			"'flask.string_literal'\n", // and inside a string literal
	})
	s, err := Index(t.Context(), root, "pkg:pypi/flask")
	if err != nil {
		t.Fatal(err)
	}
	got := symbolNames(s)
	if got["flask.something"] != 0 {
		t.Errorf("mid-identifier match leaked: %v", got)
	}
	if got["flask.string_literal"] != 0 {
		t.Errorf("string-literal match leaked: %v", got)
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
	s, err := Index(t.Context(), root, "pkg:pypi/flask")
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
	s, err := Index(t.Context(), root, "pkg:pypi/engine-io-parser")
	if err != nil {
		t.Fatal(err)
	}
	if symbolNames(s)["decode"] == 0 {
		t.Errorf("hyphen/underscore normalisation failed: %v", symbolNames(s))
	}
}

func TestPyDistributionCase(t *testing.T) {
	// PyPI distribution name "Flask"; module import is lowercase.
	root := writeTree(t, map[string]string{
		"a.py": "from flask import jsonify\n",
	})
	s, err := Index(t.Context(), root, "pkg:pypi/Flask")
	if err != nil {
		t.Fatal(err)
	}
	if symbolNames(s)["jsonify"] == 0 {
		t.Errorf("case-insensitive distribution match failed: %v", symbolNames(s))
	}
}

func TestPyCuratedChainedBeforeHeuristic(t *testing.T) {
	// PyYAML installs module `yaml`; the heuristic alone would guess
	// `pyyaml`. curated.Python() supplies the correct mapping and Chain
	// merges both, so `import yaml` matches.
	root := writeTree(t, map[string]string{
		"a.py": "import yaml\nyaml.safe_load(f)\n",
	})
	s, err := Index(t.Context(), root, "pkg:pypi/PyYAML@6.0")
	if err != nil {
		t.Fatal(err)
	}
	got := symbolNames(s)
	if got["yaml"] == 0 || got["yaml.safe_load"] == 0 {
		t.Errorf("curated PyYAML→yaml mapping not applied: %v", got)
	}
}
