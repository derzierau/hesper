package auth

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"html/template"
	"net/http"

	"github.com/derzierau/hesper/relay/internal/identity"
)

// The browser side of sign-in: the approval page, the page after it and
// one page per failure, all in one layout. The stylesheet is inline and
// the CSP allows it by hash; there are no scripts, fonts or images.
// Colors are the app's Daylight and Dusk tokens (app/Sources/Hesper/UI/Theme.swift).
const stylesheet = `
:root{color-scheme:light dark;--bg:#F6F5FA;--card:#FFFFFF;--surface:#ECEBF3;--line:#DFDEEA;--text:#1B1D33;--text2:#424457;--dim:#61668C;--signal:#C9305C;--accent:#4B55D6;--on-accent:#FFFFFF;--done:#17804F;--error:#D93636}
@media (prefers-color-scheme:dark){:root{--bg:#0F1020;--card:#13152A;--surface:#171A2E;--line:#272B48;--text:#D7DBF5;--text2:#B3B6CF;--dim:#7A80A8;--signal:#FF6F91;--accent:#8F9CFF;--on-accent:#0F1020;--done:#7FE0A8;--error:#FF5D5D}}
*{box-sizing:border-box}
html{-webkit-text-size-adjust:100%;text-size-adjust:100%}
body{margin:0;min-height:100vh;min-height:100dvh;display:flex;justify-content:center;background:var(--bg);color:var(--text);font:400 16px/1.45 -apple-system,BlinkMacSystemFont,system-ui,"Segoe UI",Roboto,"Helvetica Neue",sans-serif;padding:max(32px,env(safe-area-inset-top)) max(20px,env(safe-area-inset-right)) max(24px,env(safe-area-inset-bottom)) max(20px,env(safe-area-inset-left))}
main{width:100%;max-width:400px}
@media (min-width:600px){body{align-items:center}main{max-width:440px;padding:40px;background:var(--card);border:1px solid var(--line);border-radius:14px}}
.mark{display:flex;align-items:flex-end;gap:3px;margin:0 0 28px;font-size:40px;font-weight:600;line-height:.8;letter-spacing:-.01em}
.mark i{width:9px;height:9px;border-radius:2px;background:var(--signal)}
@media (prefers-reduced-motion:no-preference){.pulse i{animation:pulse 2s ease-in-out infinite}}
@keyframes pulse{50%{opacity:.4}}
h1{margin:0 0 8px;font-size:24px;font-weight:600;line-height:1.2;letter-spacing:-.01em}
p{margin:0 0 12px;color:var(--text2)}
p:last-child{margin-bottom:0}
b{font-weight:600;color:var(--text)}
.panel{margin:20px 0;padding:16px;background:var(--surface);border:1px solid var(--line);border-radius:10px}
.label{margin:0 0 2px;font-size:13px;font-weight:500;color:var(--dim)}
.panel strong{display:block;font-size:17px;font-weight:600;color:var(--text);overflow-wrap:anywhere}
.role{margin:2px 0 0;font-size:14px}
hr{margin:14px 0;border:0;border-top:1px solid var(--line)}
.code{font:600 30px/1.2 ui-monospace,"SF Mono",SFMono-Regular,Menlo,Consolas,monospace;letter-spacing:.08em;font-variant-numeric:slashed-zero;color:var(--text);overflow-wrap:anywhere}
.hint{margin:4px 0 0;font-size:14px}
form{margin:0}
button{display:block;width:100%;min-height:50px;padding:0 16px;border:0;border-radius:10px;background:var(--accent);color:var(--on-accent);font-family:inherit;font-size:17px;font-weight:600;cursor:pointer;-webkit-appearance:none;appearance:none}
button:active{opacity:.85}
button:focus-visible{outline:2px solid var(--accent);outline-offset:3px}
.note{margin:16px 0 0;font-size:14px;color:var(--dim)}
.note+.note{margin-top:8px}
.state{display:flex;align-items:center;gap:8px;margin:0 0 8px;font-size:13px;font-weight:500;color:var(--dim)}
.state i{width:7px;height:7px;border-radius:1.5px}
.done i{background:var(--done)}
.error i{background:var(--error)}
`

// styleSource is the CSP source that allows the stylesheet.
var styleSource = func() string {
	sum := sha256.Sum256([]byte(stylesheet))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}()

var pages = template.Must(template.New("layout").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">
<meta name="color-scheme" content="light dark">
<title>{{.Title}} · Hesper</title>
<style>` + stylesheet + `</style>
</head><body><main>
<div class="mark{{if .Approve}} pulse{{end}}" role="img" aria-label="Hesper">h<i></i></div>
{{with .Approve}}<h1>Approve this device</h1>
<p>Signed in to GitHub as <b>{{.Login}}</b>.</p>
<div class="panel">
<p class="label">Device</p>
<strong>{{.Name}}</strong>
<p class="role">{{.Role}}</p>
<hr>
<p class="label">Code</p>
<div class="code">{{.Code}}</div>
<p class="hint">Check that {{.Name}} shows the same code.</p>
</div>
<form method="post" action="/auth/approve"><input type="hidden" name="state" value="{{.State}}"><input type="hidden" name="enrollment" value="{{.Enrollment}}"><button type="submit">Approve</button></form>
<p class="note">Approve only if you started this sign-in on {{.Name}} yourself. If you didn't, close this window.</p>
<p class="note">Not you? Sign out of GitHub in this browser, then start again on {{.Name}}.</p>
{{else}}<p class="state {{.Tone}}"><i></i>{{.Label}}</p>
<h1>{{.Title}}</h1>
{{range .Lines}}<p>{{.}}</p>
{{end}}{{end}}</main></body></html>
`))

// view is one page: the approval form, or a title and what to know.
type view struct {
	Title   string
	Tone    string // done or error
	Label   string
	Lines   []string
	Approve *approval
}
type approval struct{ Login, Name, Role, Code, State, Enrollment string }

// roleText says what a device of role may do, in the owner's words.
func roleText(role identity.Role) string {
	if role == identity.Host {
		return "A Mac that runs your agents. Your other devices can reach it once it lets them in."
	}
	return "Will be able to answer and start agents on the Macs that approve it."
}

func render(w http.ResponseWriter, status int, v view) {
	var b bytes.Buffer
	if err := pages.Execute(&b, v); err != nil {
		http.Error(w, "The sign-in page failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = b.WriteTo(w)
}

func approvedPage(name string, again bool) view {
	if again {
		return view{Title: "Already approved", Tone: "done", Label: "Approved", Lines: []string{name + " is already approved. Return to it. You can close this window."}}
	}
	return view{Title: "Device approved", Tone: "done", Label: "Approved", Lines: []string{"Return to " + name + ". You can close this window."}}
}

// problem is a browser-facing failure: its status, the app callback
// status that reports it ("" when the app is not sent back) and its page.
type problem struct {
	status   int
	callback string
	title    string
	what     string
	next     string
}

func (p problem) view() view {
	label := "Sign-in stopped"
	if p.status == http.StatusTooManyRequests || p.status == http.StatusServiceUnavailable {
		label = "Try again soon"
	}
	return view{Title: p.title, Tone: "error", Label: label, Lines: []string{p.what, p.next}}
}

// withStatus keeps a page and its words under another HTTP status.
func (p problem) withStatus(status int) problem {
	p.status = status
	return p
}

var (
	expiredProblem      = problem{400, "expired", "This sign-in has expired", "Sign-in links work once, for 10 minutes.", "Start again on your device."}
	wrongBrowserProblem = problem{403, "", "This browser can't finish the sign-in", "The sign-in started in another browser, or this browser blocks cookies for this site.", "Start again on your device and finish in the browser it opens, with cookies allowed."}
	deniedProblem       = problem{400, "denied", "GitHub sign-in was cancelled", "GitHub didn't sign you in, so nothing was approved.", "To try again, start again on your device."}
	githubProblem       = problem{403, "failed", "GitHub didn't confirm your account", "The relay couldn't check your GitHub sign-in.", "Start again on your device. If it keeps happening, try again later."}
	originProblem       = problem{403, "", "This approval came from another site", "The relay only takes Approve from its own page, so nothing was approved.", "Start again on your device and approve on the page it opens."}
	busyProblem         = problem{503, "failed", "The relay is busy", "Too many sign-ins are open right now.", "Wait a minute, then start again on your device."}
	rateProblem         = problem{429, "failed", "Too many sign-ins", "The relay is getting more requests than it lets through.", "Wait a minute, then reload this page."}
	notFoundProblem     = problem{404, "", "Page not found", "This address isn't part of a sign-in.", "Start again on your device."}
)

func notAllowedProblem(login string) problem {
	if login == "" {
		login = "This GitHub account"
	}
	return problem{403, "not_allowed", login + " can't use this relay", "This relay only lets in the GitHub accounts its owner has listed.", "Sign out of GitHub and use another account, or ask the relay's owner to let it in."}
}
