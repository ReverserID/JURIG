package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FridaPresetTool runs a built-in, battle-tested Frida script so the agent
// gets dynamic instrumentation without hand-writing JS every time.
type FridaPresetTool struct{}

func (t *FridaPresetTool) Name() string { return "frida_preset" }
func (t *FridaPresetTool) Description() string {
	return "Run a built-in Frida script on a USB device (needs frida-server running). Presets: " +
		"ssl_unpin (defeat SSL/cert pinning), list_classes (arg=filter substring), dump_class (arg=fully.qualified.Class), " +
		"hook (arg=fully.qualified.Class!method), trace_http (log OkHttp/HttpURLConnection requests). " +
		"Set spawn=true to launch the package fresh. Returns captured console output."
}
func (t *FridaPresetTool) Schema() map[string]any {
	return schema(map[string]any{
		"preset":      strProp("one of: ssl_unpin | list_classes | dump_class | hook | trace_http"),
		"target":      strProp("package name (spawn) or process name (attach)"),
		"arg":         strProp("preset argument: filter, class name, or Class!method"),
		"spawn":       boolProp("launch target fresh (-f) instead of attach"),
		"run_seconds": map[string]any{"type": "integer", "description": "run window before detach (default 20)"},
	}, "preset", "target")
}

func (t *FridaPresetTool) Run(ctx context.Context, input json.RawMessage, env *Env) (string, error) {
	var in struct {
		Preset     string `json:"preset"`
		Target     string `json:"target"`
		Arg        string `json:"arg"`
		Spawn      bool   `json:"spawn"`
		RunSeconds int    `json:"run_seconds"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", err
	}
	script, err := fridaScript(in.Preset, in.Arg)
	if err != nil {
		return "", err
	}
	bin, err := env.ResolveBin("frida")
	if err != nil {
		return "", err
	}
	if in.RunSeconds == 0 {
		in.RunSeconds = 20
	}
	if err := os.MkdirAll(env.WorkDir, 0o755); err != nil {
		return "", err
	}
	scriptPath := filepath.Join(env.WorkDir, "frida_"+in.Preset+".js")
	if err := os.WriteFile(scriptPath, []byte(script), 0o644); err != nil {
		return "", err
	}

	args := []string{"-U", "-q", "-l", scriptPath}
	if in.Spawn {
		args = append(args, "-f", in.Target)
	} else {
		args = append(args, "-n", in.Target)
	}
	out, err := runCmd(ctx, env, time.Duration(in.RunSeconds+10)*time.Second, env.WorkDir, bin, args...)
	if err != nil && strings.Contains(err.Error(), "timeout") {
		return out, nil // intentional run window elapsed
	}
	return out, err
}

func fridaScript(preset, arg string) (string, error) {
	switch preset {
	case "ssl_unpin":
		return sslUnpinJS, nil
	case "list_classes":
		return fmt.Sprintf(listClassesJS, jsStr(arg)), nil
	case "dump_class":
		if arg == "" {
			return "", fmt.Errorf("dump_class needs arg=fully.qualified.Class")
		}
		return fmt.Sprintf(dumpClassJS, jsStr(arg)), nil
	case "hook":
		cls, method, ok := splitBang(arg)
		if !ok {
			return "", fmt.Errorf("hook needs arg=fully.qualified.Class!method")
		}
		return fmt.Sprintf(hookMethodJS, jsStr(cls), jsStr(method)), nil
	case "trace_http":
		return traceHTTPJS, nil
	default:
		return "", fmt.Errorf("unknown preset %q", preset)
	}
}

func splitBang(s string) (cls, method string, ok bool) {
	i := strings.LastIndexByte(s, '!')
	if i < 0 {
		i = strings.LastIndexByte(s, '#') // tolerate Class#method too
	}
	if i <= 0 || i >= len(s)-1 {
		return "", "", false
	}
	return s[:i], s[i+1:], true
}

// jsStr escapes a string for safe embedding in JS source.
func jsStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

const sslUnpinJS = `// Jurig: universal Android SSL pinning bypass
Java.perform(function () {
  function log(m){ console.log("[ssl_unpin] " + m); }
  try {
    var X509TrustManager = Java.use('javax.net.ssl.X509TrustManager');
    var SSLContext = Java.use('javax.net.ssl.SSLContext');
    var TrustManager = Java.registerClass({
      name: 'com.jurig.TrustAll',
      implements: [X509TrustManager],
      methods: {
        checkClientTrusted: function () {},
        checkServerTrusted: function () {},
        getAcceptedIssuers: function () { return []; }
      }
    });
    var tms = [TrustManager.$new()];
    var init = SSLContext.init.overload(
      '[Ljavax.net.ssl.KeyManager;', '[Ljavax.net.ssl.TrustManager;', 'java.security.SecureRandom');
    init.implementation = function (km, tm, sr) { log('SSLContext.init hooked'); init.call(this, km, tms, sr); };
  } catch (e) { log('trustmanager: ' + e); }

  // OkHttp3 CertificatePinner
  try {
    var CP = Java.use('okhttp3.CertificatePinner');
    CP.check.overload('java.lang.String', 'java.util.List').implementation = function (h, p) { log('okhttp pin skipped: ' + h); };
    log('okhttp CertificatePinner hooked');
  } catch (e) {}

  // TrustManagerImpl (Conscrypt) verifyChain
  try {
    var TMI = Java.use('com.android.org.conscrypt.TrustManagerImpl');
    TMI.verifyChain.implementation = function (chain, atype, host, ce, ocsp, tls) { log('conscrypt verifyChain skipped: ' + host); return chain; };
    log('conscrypt TrustManagerImpl hooked');
  } catch (e) {}
  log('installed');
});`

const listClassesJS = `// Jurig: list loaded classes matching a filter
Java.perform(function () {
  var filter = %s;
  var n = 0;
  Java.enumerateLoadedClasses({
    onMatch: function (name) {
      if (!filter || name.toLowerCase().indexOf(String(filter).toLowerCase()) !== -1) { console.log(name); n++; }
    },
    onComplete: function () { console.log('[list_classes] ' + n + ' matches'); }
  });
});`

const dumpClassJS = `// Jurig: dump methods + fields of a class
Java.perform(function () {
  var name = %s;
  try {
    var C = Java.use(name);
    console.log('== ' + name + ' ==');
    C.class.getDeclaredMethods().forEach(function (m) { console.log('  method: ' + m.toString()); });
    C.class.getDeclaredFields().forEach(function (f) { console.log('  field:  ' + f.toString()); });
  } catch (e) { console.log('[dump_class] ' + e); }
});`

const hookMethodJS = `// Jurig: hook every overload of a method, log args + return
Java.perform(function () {
  var name = %s, method = %s;
  try {
    var C = Java.use(name);
    C[method].overloads.forEach(function (ov) {
      ov.implementation = function () {
        var a = [];
        for (var i = 0; i < arguments.length; i++) { a.push(String(arguments[i])); }
        console.log('[hook] ' + name + '.' + method + '(' + a.join(', ') + ')');
        var r = ov.apply(this, arguments);
        console.log('[hook]  => ' + String(r));
        return r;
      };
    });
    console.log('[hook] armed ' + name + '.' + method);
  } catch (e) { console.log('[hook] ' + e); }
});`

const traceHTTPJS = `// Jurig: trace HTTP via OkHttp Request + HttpURLConnection
Java.perform(function () {
  try {
    var RB = Java.use('okhttp3.Request$Builder');
    RB.url.overload('java.lang.String').implementation = function (u) { console.log('[http] okhttp url: ' + u); return this.url(u); };
  } catch (e) {}
  try {
    var URL = Java.use('java.net.URL');
    URL.openConnection.overload().implementation = function () { console.log('[http] URL.openConnection: ' + this.toString()); return this.openConnection(); };
  } catch (e) {}
  console.log('[trace_http] armed');
});`
