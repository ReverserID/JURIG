package tools

import (
	"context"
	"encoding/json"
	"fmt"
)

// ProxyTool drives the native MITM proxy for dynamic network capture. The
// agent typically: start → configure the device (adb proxy + install CA) →
// frida_preset ssl_unpin → drive the app → flows.
type ProxyTool struct{}

func (t *ProxyTool) Name() string { return "proxy" }
func (t *ProxyTool) Description() string {
	return "Native MITM HTTPS proxy for live traffic capture. Actions: " +
		"'start' (returns listen addr + CA path — then set the device proxy and install the CA), " +
		"'flows' (return recent captured request/response pairs), 'stop', 'status', 'clear'. " +
		"Typical dynamic flow: proxy start → adb shell settings put global http_proxy <host:port> → adb push <ca> + install → frida_preset ssl_unpin → open the app → proxy flows."
}
func (t *ProxyTool) Schema() map[string]any {
	return schema(map[string]any{
		"action": strProp("start | flows | stop | status | clear"),
		"port":   map[string]any{"type": "integer", "description": "listen port for start (default 8888)"},
		"limit":  map[string]any{"type": "integer", "description": "flows to return (default 40)"},
	}, "action")
}
func (t *ProxyTool) Run(_ context.Context, input json.RawMessage, env *Env) (string, error) {
	var in struct {
		Action string `json:"action"`
		Port   int    `json:"port"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", err
	}
	if env.Proxy == nil {
		return "", fmt.Errorf("proxy unavailable in this mode")
	}
	switch in.Action {
	case "start":
		addr, ca, err := env.Proxy.Start(in.Port)
		if err != nil {
			return "", err
		}
		env.emit("cmd", "proxy listening on "+addr)
		return fmt.Sprintf(`proxy started on %s
CA cert: %s

Next steps to capture device TLS:
  adb push %s /sdcard/jurig-ca.pem      (then install via Settings > Security > Install cert, or use a system-CA method)
  adb shell settings put global http_proxy <HOST_IP>:%s   (emulator: HOST_IP=10.0.2.2)
  frida_preset ssl_unpin on the target package to defeat pinning
Then open/drive the app and call proxy action=flows.`, addr, ca, ca, portOf(addr)), nil
	case "flows":
		if in.Limit == 0 {
			in.Limit = 40
		}
		return env.Proxy.FlowsText(in.Limit), nil
	case "stop":
		if err := env.Proxy.Stop(); err != nil {
			return "", err
		}
		return "proxy stopped", nil
	case "status":
		return fmt.Sprintf("running=%v flows=%d", env.Proxy.Running(), env.Proxy.Count()), nil
	case "clear":
		// Clear is optional on the interface; FlowsText/Count still work.
		return "ok", nil
	default:
		return "", fmt.Errorf("unknown action %q (start|flows|stop|status|clear)", in.Action)
	}
}

func portOf(addr string) string {
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			return addr[i+1:]
		}
	}
	return addr
}
