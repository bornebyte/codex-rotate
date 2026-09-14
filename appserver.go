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
	"strings"
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
	out := fmt.Sprintf("%dh%02dm (%s)", h, m, t.Local().Format("Jan 2 15:04"))
	if wd := formatWindowDuration(w.WindowDurationMins); wd != "" {
		out += fmt.Sprintf(" [%s window]", wd)
	}
	return out
}

// formatWindowDuration renders a window's actual length from the minutes
// app-server reports for it.
//
// This exists because "primary = 5h, secondary = weekly" — which is what
// Pro/Team plans report, and what an earlier version of this file assumed
// for the column headers — turned out not to hold for free/Go plan
// accounts: observed data shows a single `primary` window with resets
// anywhere from ~10 to ~30 days out, and no `secondary` window at all.
// Rather than assert a duration that's sometimes just wrong, the table
// headers now say PRIMARY/SECONDARY (see cmdStats) and the *real* window
// length is printed here, per row, from whatever the account reports.
func formatWindowDuration(mins float64) string {
	if mins <= 0 {
		return ""
	}
	hours := mins / 60
	if hours < 24 {
		return fmt.Sprintf("%.0fh", hours)
	}
	return fmt.Sprintf("%.0fd", hours/24)
}

// windowWarning flags a window that's nearly exhausted, mirroring the "⚠"
// convention checkDrift already uses elsewhere in this tool. It's plain
// text on purpose (unlike the LEFT column below) so the warning still
// shows up even with colors off or output piped to a file.
func windowWarning(w *rateLimitWindow) string {
	if w != nil && w.UsedPercent >= 90 {
		return "⚠ "
	}
	return ""
}

// ---------- colored "remaining" column ----------
//
// remaining% is just 100-used%, but showing it as its own colored column
// answers "how worried should I be?" at a glance instead of making you do
// the subtraction and compare it to a mental threshold yourself.

const (
	ansiRed    = "\x1b[31m"
	ansiYellow = "\x1b[33m"
	ansiGreen  = "\x1b[32m"
	ansiReset  = "\x1b[0m"

	// Thresholds are on *remaining* percent, not used percent.
	remainingCriticalPct = 10.0 // <=10% left: red — you're about to get locked out
	remainingLowPct      = 30.0 // <=30% left: yellow — worth planning around
)

// remainingColor picks the ANSI color for a remaining-percent value.
func remainingColor(remainingPct float64) string {
	switch {
	case remainingPct <= remainingCriticalPct:
		return ansiRed
	case remainingPct <= remainingLowPct:
		return ansiYellow
	default:
		return ansiGreen
	}
}

// stdoutSupportsColor is a minimal, stdlib-only isatty check: color is
// switched off automatically when stdout is redirected to a file or pipe
// (raw ANSI codes in a saved log are just noise), and NO_COLOR is honored
// per the https://no-color.org convention if the person sets it.
func stdoutSupportsColor() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	info, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// remainingCell renders one window's remaining-quota percentage as a table
// cell, colored red/yellow/green by how urgent it is. The plain "43%" text
// is identical whether or not colorsEnabled is true — only the ANSI
// wrapping is conditional — so piping the output through `cat` or a NO_COLOR
// terminal never loses information, just the color.
func remainingCell(w *rateLimitWindow, colorsEnabled bool) tcell {
	if w == nil {
		return plainCell("-")
	}
	remainingPct := 100 - w.UsedPercent
	text := fmt.Sprintf("%.0f%%", remainingPct)
	return colorCell(text, remainingColor(remainingPct), colorsEnabled)
}

// summarizeAppServerError turns a raw app-server error into one short,
// single-line message safe to put in a table cell.
//
// For HTTP failures, app-server's error message embeds the *entire*
// upstream response — status line, headers, and a multi-line JSON body —
// which is exactly what you want in a log but unreadable jammed into a
// table's STATUS column. Known authentication failures (an expired or
// unparsable stored token — the most common real-world cause, since
// codex-rotate only ever reads a profile's stored auth.json and can't
// refresh it for you) get a plain-English, actionable message. Anything
// else is collapsed onto one line and capped in length so a single bad
// row can't blow out the whole table's width.
func summarizeAppServerError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	lower := strings.ToLower(msg)
	const howToFix = "switch into it, run `codex login`, then `capture` it again"
	switch {
	case strings.Contains(lower, "token_expired"):
		return "auth token expired — " + howToFix
	case strings.Contains(lower, "could not parse your authentication token"),
		strings.Contains(lower, "unauthorized_unknown"):
		return "auth token invalid — " + howToFix
	case strings.Contains(lower, "401"):
		return "unauthorized (401) — token likely needs refreshing; " + howToFix
	}
	collapsed := strings.Join(strings.Fields(msg), " ")
	const maxLen = 140
	if len(collapsed) > maxLen {
		collapsed = collapsed[:maxLen] + "…"
	}
	return collapsed
}