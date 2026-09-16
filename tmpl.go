// Copyright (c) 2014-2015, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package main

import "html/template"

var tmpl *template.Template

func loadTemplates() {
	for name, s := range templates {
		var t *template.Template
		if tmpl == nil {
			tmpl = template.New(name)
		}
		if name == tmpl.Name() {
			t = tmpl
		} else {
			t = tmpl.New(name)
		}
		if _, err := t.Parse(s); err != nil {
			panic("could not load templates")
		}
	}
}

var templates = map[string]string{
	"/": `<html>
<body style="text-align:center">
<pre style="display:inline-block;text-align:left;margin:2em 2em 2em 0">
Set up an alias:

    $ alias pcat='curl -F "{{.FieldName}}=&lt;-" {{.SiteURL}}'

Upload a new paste:

    $ echo foo | pcat
    {{.SiteURL}}/a63d03b9

Fetch it:

    $ curl {{.SiteURL}}/a63d03b9
    foo

You can also use the <a href="form">web form</a>.
{{if gt .MaxSize 0.0}}
The maximum size per paste is {{.MaxSize}}.
{{end}}{{if gt .LifeTime 0}}
Each paste will be deleted after {{.LifeTime}}.
{{end}}
<a href="http://github.com/mvdan/pastecat">github.com/mvdan/pastecat</a>
</pre>
</body>
</html>
`,
	"/edit": `<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.ID}} - pastecat</title>
<style>
:root {
	color-scheme: light dark;
}
html {
	height: 100%;
	margin: 0;
	background: #fff;
}
body {
	height: 100%;
	margin: 0;
	box-sizing: border-box;
	padding: 1ex;
	background: rgba(219, 231, 245, 0.5);
}
#wrap {
	display: flex;
	height: 100%;
}
#gutter {
	box-sizing: border-box;
	flex: none;
	overflow: hidden;
	padding: 1ex 0.75ex;
	background: #eef1f5;
	border-radius: 3px 0 0 3px;
	user-select: none;
}
#gutterLines {
	margin: 0;
	text-align: right;
	color: #99a;
	font-family: SFMono-Regular, Menlo, Consolas, "Liberation Mono", monospace;
	font-size: 14px;
	line-height: 1.5;
	will-change: transform;
}
#gutter + #editor {
	border-radius: 0 3px 3px 0;
}
#editor {
	box-sizing: border-box;
	display: block;
	flex: 1 1 auto;
	width: 100%;
	min-width: 0;
	height: 100%;
	margin: 0;
	padding: 1ex;
	border: 0;
	border-radius: 3px;
	outline: none;
	resize: none;
	background: #fff;
	color: #111;
	font-family: SFMono-Regular, Menlo, Consolas, "Liberation Mono", monospace;
	font-size: 14px;
	line-height: 1.5;
	tab-size: 4;
}
#status {
	position: fixed;
	right: 8px;
	bottom: 6px;
	font-family: monospace;
	font-size: 12px;
	color: #888;
	pointer-events: none;
}
@media (prefers-color-scheme: dark) {
	html {
		background: #1f1f1f;
	}
	#editor {
		background: #1f1f1f;
		color: #d4d4d4;
	}
	#gutter {
		background: #2a2a2a;
	}
	#gutterLines {
		color: #666;
	}
	#status {
		color: #777;
	}
}
</style>
</head>
<body>
<div id="wrap">
{{if .Number}}<div id="gutter"><pre id="gutterLines">1</pre></div>{{end}}
<textarea id="editor" spellcheck="false" autofocus{{if .Number}} wrap="off"{{end}}>{{.Content}}</textarea>
</div>
<div id="status"></div>
<script>
(function () {
	var editor = document.getElementById('editor');
	var status = document.getElementById('status');
	var gutter = document.getElementById('gutterLines');
	var timer = null;
	var dirty = false;
	var saving = false;

	function updateGutter() {
		if (!gutter) { return; }
		var count = editor.value.split('\n').length;
		var lines = new Array(count);
		for (var i = 1; i <= count; i++) { lines[i - 1] = i; }
		gutter.textContent = lines.join('\n');
		gutter.style.transform = 'translateY(' + (-editor.scrollTop) + 'px)';
	}

	function setStatus(text, ok) {
		status.textContent = text;
		status.style.color = ok ? '' : '#c00';
	}

	function save() {
		if (saving) { return; }
		saving = true;
		dirty = false;
		fetch(location.pathname, {
			method: 'POST',
			headers: {'Content-Type': 'text/plain; charset=utf-8'},
			body: editor.value
		}).then(function (res) {
			if (!res.ok) { throw new Error(res.status + ' ' + res.statusText); }
			setStatus('saved', true);
		}).catch(function (err) {
			dirty = true;
			setStatus('save failed: ' + err.message, false);
		}).then(function () {
			saving = false;
			if (dirty) { save(); }
		});
	}

	editor.addEventListener('input', function () {
		dirty = true;
		setStatus('...', true);
		updateGutter();
		clearTimeout(timer);
		timer = setTimeout(save, 800);
	});

	editor.addEventListener('scroll', function () {
		if (gutter) {
			gutter.style.transform = 'translateY(' + (-editor.scrollTop) + 'px)';
		}
	});

	editor.addEventListener('keydown', function (e) {
		if ((e.ctrlKey || e.metaKey) && e.key === 's') {
			e.preventDefault();
			clearTimeout(timer);
			save();
			return;
		}
		if (e.key === 'Tab') {
			e.preventDefault();
			var start = editor.selectionStart;
			var end = editor.selectionEnd;
			editor.value = editor.value.slice(0, start) + '\t' + editor.value.slice(end);
			editor.selectionStart = editor.selectionEnd = start + 1;
			dirty = true;
			updateGutter();
		}
	});

	updateGutter();

	window.addEventListener('pagehide', function () {
		if (!dirty) { return; }
		var blob = new Blob([editor.value], {type: 'text/plain; charset=utf-8'});
		navigator.sendBeacon(location.pathname, blob);
	});
})();
</script>
</body>
</html>
`,
	"/form": `<html>
<body style="text-align:center">
<div style="inline-block">
	<form action="{{.SiteURL}}/redirect" method="post" enctype="multipart/form-data">
		<textarea cols=80 rows=24 name="{{.FieldName}}"></textarea>
		<br/>
		<button type="submit">Paste text</button>
	</form>
	<br/>
	<form action="{{.SiteURL}}/redirect" method="post" enctype="multipart/form-data">
		<input type="file" name="{{.FieldName}}"></input>
		<button type="submit">Paste file</button>
	</form>
</div>
</body>
</html>
`,
}
