package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// ---------- app-server JSON-RPC client ----------
//
// `codex-rotate stats` needs live quota numbers (used%, reset time) for
// every profile, including ones that are currently parked. That data lives
// behind the same backend the Codex CLI's own `/status` hits, and the only
// *local* way to reach it — short of reverse-engineering an HTTP endpoint —
// is `codex app-server`: a JSON-RPC-over-stdio process the official CLI,
// the VS Code extension, and Codex Desktop all speak. It's documented at
// https://developers.openai.com/codex/app-server.
//
// Wire format is JSON-RPC 2.0 over newline-delimited stdio, with the
// `"jsonrpc":"2.0"` member omitted (per Codex's own docs):
//
//	-> {"method":"initialize","id":1,"params":{"clientInfo":{...}}}
//	<- {"id":1,"result":{...}}
//	-> {"method":"initialized","params":{}}                    (notification)
//	-> {"method":"account/rateLimits/read","id":2,"params":{}}
//	<- {"id":2,"result":{"rateLimits":{"primary":{...},"secondary":{...}}}}
//
// Crucially, none of this ever touches ~/.codex/auth.json: we copy a
// profile's credential bytes into a scratch CODEX_HOME and point a
// throwaway app-server process at that. An inactive profile's quota can
// therefore be read without any park/switch dance, and the live active
// session is never disturbed either.
//
// One caveat worth knowing if this ever breaks: the exact field names on
// `initialize`/`account/read` have already shifted across Codex releases
// (see openai/codex#28143 and the app-server changelog), and this client
// deliberately degrades rather than hard-fails when a field it doesn't
// recognize shows up — see extractAccountInfo below.

type rpcRequest struct {
	Method string      `json:"method"`
	ID     int         `json:"id,omitempty"`
	Params interface{} `json:"params"`
}

type rpcMessage struct {
	ID     *int            `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcErrorObj    `json:"error,omitempty"`
}

type rpcErrorObj struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type appServerClient struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Scanner
	nextID int
}

func startAppServerClient(ctx context.Context, codexHome string) (*appServerClient, error) {
	cmd := exec.CommandContext(ctx, "codex", "app-server")
	// CODEX_HOME redirects the child's view of ~/.codex entirely — it never
	// sees, and can't touch, the real one.
	cmd.Env = append(os.Environ(), "CODEX_HOME="+codexHome)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start `codex app-server`: %w", err)
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	return &appServerClient{cmd: cmd, stdin: stdin, stdout: scanner, nextID: 1}, nil
}

func (c *appServerClient) writeLine(v interface{}) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = c.stdin.Write(data)
	return err
}

// call sends a request and blocks until the response with the matching id
// arrives, skipping over any notifications — or responses to earlier
// calls — that show up first. If the parent context's deadline fires, the
// child process gets killed (exec.CommandContext's doing), which closes
// stdout and unblocks the scan loop with an error rather than a hang.
func (c *appServerClient) call(method string, params interface{}) (json.RawMessage, error) {
	if params == nil {
		params = map[string]interface{}{}
	}
	id := c.nextID
	c.nextID++
	if err := c.writeLine(rpcRequest{Method: method, ID: id, Params: params}); err != nil {
		return nil, fmt.Errorf("write %s: %w", method, err)
	}

	for c.stdout.Scan() {
		line := bytes.TrimSpace(c.stdout.Bytes())
		if len(line) == 0 {
			continue
		}
		var msg rpcMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			// Not every line on app-server's stdout is guaranteed to be a
			// well-formed JSON-RPC frame — skip stray output rather than
			// failing the whole call over it.
			continue
		}
		if msg.ID == nil || *msg.ID != id {
			continue // a notification, or a response to a different call
		}
		if msg.Error != nil {
			return nil, fmt.Errorf("%s (code %d)", msg.Error.Message, msg.Error.Code)
		}
		return msg.Result, nil
	}
	if err := c.stdout.Err(); err != nil {
		return nil, fmt.Errorf("read %s response: %w", method, err)
	}
	return nil, fmt.Errorf("%s: app-server closed its output before responding", method)
}

func (c *appServerClient) notify(method string, params interface{}) error {
	if params == nil {
		params = map[string]interface{}{}
	}
	return c.writeLine(rpcRequest{Method: method, Params: params})
}

func (c *appServerClient) close() {
	c.stdin.Close()
	if c.cmd.Process != nil {
		c.cmd.Process.Kill()
	}
	c.cmd.Wait()
}

// ---------- rate limit / account shapes ----------

type rateLimitWindow struct {
	UsedPercent        float64 `json:"usedPercent"`
	WindowDurationMins float64 `json:"windowDurationMins"`
	ResetsAt           int64   `json:"resetsAt"`
}

type rateLimitsResult struct {
	RateLimits struct {
		Primary   *rateLimitWindow `json:"primary"`
		Secondary *rateLimitWindow `json:"secondary"`
	} `json:"rateLimits"`
}

// accountStats is one row of `codex-rotate stats` output.
type accountStats struct {
	Name      string
	Email     string
	Plan      string
	Primary   *rateLimitWindow // conventionally the rolling 5-hour window
	Secondary *rateLimitWindow // conventionally the rolling 7-day window
	Err       error
}

// extractAccountInfo pulls email/plan out of account/read's result without
// committing to one exact schema, since those field names have already
// shifted between Codex releases. Missing fields just come back blank
// instead of breaking the row.
func extractAccountInfo(raw json.RawMessage) (email, plan string) {
	var generic map[string]interface{}
	if err := json.Unmarshal(raw, &generic); err != nil {
		return "", ""
	}
	candidates := []map[string]interface{}{generic}
	if acct, ok := generic["account"].(map[string]interface{}); ok {
		candidates = append(candidates, acct)
	}
	for _, m := range candidates {
		if email == "" {
			if v, ok := m["email"].(string); ok {
				email = v
			}
		}
		if plan == "" {
			for _, key := range []string{"planType", "plan_type", "plan"} {
				if v, ok := m[key].(string); ok {
					plan = v
					break
				}
			}
		}
	}
	return email, plan
}

// fetchProfileStats queries live quota for one profile's auth.json without
// ever touching the real file: it copies the bytes into a scratch CODEX_HOME
// and points a throwaway `codex app-server` process at that instead.
func fetchProfileStats(ctx context.Context, name, authFile string) *accountStats {
	stats := &accountStats{Name: name}

	data, err := os.ReadFile(authFile)
	if err != nil {
		stats.Err = fmt.Errorf("read %s: %w", authFile, err)
		return stats
	}

	scratch, err := os.MkdirTemp("", "codex-rotate-stats-*")
	if err != nil {
		stats.Err = fmt.Errorf("create scratch dir: %w", err)
		return stats
	}
	defer os.RemoveAll(scratch)

	if err := os.WriteFile(filepath.Join(scratch, "auth.json"), data, 0o600); err != nil {
		stats.Err = fmt.Errorf("stage auth.json copy: %w", err)
		return stats
	}

	client, err := startAppServerClient(ctx, scratch)
	if err != nil {
		stats.Err = err
		return stats
	}
	defer client.close()

	if _, err := client.call("initialize", map[string]interface{}{
		"clientInfo": map[string]interface{}{
			"name":    "codex-rotate",
			"title":   "codex-rotate stats",
			"version": "0.1.0",
		},
	}); err != nil {
		stats.Err = fmt.Errorf("initialize: %w", err)
		return stats
	}
	// Some app-server versions expect this before any other request;
	// harmless to send even if a given version doesn't require it.
	_ = client.notify("initialized", nil)

	result, err := client.call("account/rateLimits/read", nil)
	if err != nil {
		stats.Err = fmt.Errorf("account/rateLimits/read: %w", err)
		return stats
	}
	var parsed rateLimitsResult
	if err := json.Unmarshal(result, &parsed); err != nil {
		stats.Err = fmt.Errorf("parse rate limits: %w", err)
		return stats
	}
	stats.Primary = parsed.RateLimits.Primary
	stats.Secondary = parsed.RateLimits.Secondary

	// account/read is supplementary (email/plan for the table) — a failure
	// here shouldn't blank out the quota numbers we already have.
	if acctResult, err := client.call("account/read", nil); err == nil {
		stats.Email, stats.Plan = extractAccountInfo(acctResult)
	}

	return stats
}

// ---------- display formatting ----------

func formatPercent(w *rateLimitWindow) string {
	if w == nil {
		return "-"
	}
	return fmt.Sprintf("%.0f%%", w.UsedPercent)
}

func formatReset(w *rateLimitWindow) string {
	if w == nil || w.ResetsAt == 0 {
		return "-"
	}
	t := time.Unix(w.ResetsAt, 0)
	d := time.Until(t)
	if d <= 0 {
		return "resetting now"
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	return fmt.Sprintf("%dh%02dm (%s)", h, m, t.Local().Format("Jan 2 15:04"))
}

// windowWarning flags a window that's nearly exhausted, mirroring the "⚠"
// convention checkDrift already uses elsewhere in this tool.
func windowWarning(w *rateLimitWindow) string {
	if w != nil && w.UsedPercent >= 90 {
		return "⚠ "
	}
	return ""
}