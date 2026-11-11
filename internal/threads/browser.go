package threads

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

var profiles = struct {
	sync.Mutex
	active map[string]bool
}{active: map[string]bool{}}

// New starts one headless browser with a bounded startup. Profile paths are
// canonicalized and exclusively leased for the browser lifetime.
func New(ctx context.Context, opts Options) (result *Browser, err error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	start := opts.Now()
	b := &Browser{timeout: opts.Timeout, agentID: opts.AgentID, logger: opts.Logger, now: opts.Now, gate: make(chan struct{}, 1)}
	defer func() { err = operationError("launch", err); b.record(ctx, "launch", start, err) }()
	if opts.Timeout == 0 {
		b.timeout = 10 * time.Second
	}
	if opts.StartupTimeout == 0 {
		opts.StartupTimeout = 15 * time.Second
	}
	base, parseErr := url.Parse(opts.BaseURL)
	if parseErr != nil || base == nil || base.User != nil || base.Hostname() == "" || (base.Scheme != "https" && base.Scheme != "http") || base.RawQuery != "" || base.Fragment != "" || (base.Path != "" && base.Path != "/") || strings.ContainsAny(opts.BaseURL, "\\\r\n\t") || opts.ProfileDir == "" || opts.ExecutablePath == "" || opts.AgentID == "" || b.timeout <= 0 || opts.StartupTimeout <= 0 {
		return nil, ErrMalformedPage
	}
	b.base = base
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	release, err := leaseProfile(opts.ProfileDir, opts.AgentID)
	if err != nil {
		return nil, err
	}
	flags := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	flags = append(flags, chromedp.ExecPath(opts.ExecutablePath), chromedp.UserDataDir(release.path), chromedp.WSURLReadTimeout(opts.StartupTimeout), chromedp.Flag("disable-dev-shm-usage", true), chromedp.Flag("disable-crash-reporter", true), chromedp.Flag("disable-breakpad", true), chromedp.Flag("disable-background-networking", true))
	// Fixture runs have no external network destination, including Chrome's
	// browser-level background services which precede target interception.
	host := base.Hostname()
	ip := net.ParseIP(host)
	if host == "localhost" || (ip != nil && ip.IsLoopback()) {
		flags = append(flags, chromedp.Flag("host-resolver-rules", "MAP * ~NOTFOUND, EXCLUDE 127.0.0.1, EXCLUDE localhost, EXCLUDE [::1]"), chromedp.Flag("proxy-server", "http://127.0.0.1:9"), chromedp.Flag("proxy-bypass-list", "127.0.0.1;localhost;[::1]"))
	}
	if opts.NoSandbox {
		flags = append(flags, chromedp.Flag("no-sandbox", true))
	}
	lifetime, stopLifetime := context.WithCancel(ctx)
	allocator, stopAllocator := chromedp.NewExecAllocator(lifetime, flags...)
	browser, stopBrowser := chromedp.NewContext(allocator, chromedp.WithLogf(func(string, ...any) {}), chromedp.WithErrorf(func(string, ...any) {}))
	d := &cdpDriver{stopLifetime: stopLifetime, ctx: browser, stopBrowser: stopBrowser, stopAllocator: stopAllocator, release: release.close, base: base, timeout: b.timeout}
	b.driver = d
	startup, cancel := context.WithTimeout(browser, opts.StartupTimeout)
	defer cancel()
	// Launch on the lifetime context: canceling a child used for first Run
	// would also stop its browser connection after successful startup.
	startupTimer := time.AfterFunc(opts.StartupTimeout, stopLifetime)
	err = chromedp.Do(browser)
	startupTimer.Stop()
	if err != nil {
		_ = d.close(context.Background())
		return nil, err
	}
	events := chromedp.Events(browser, fetch.RequestPaused)
	go func() {
		for event, eventErr := range events {
			if eventErr != nil {
				return
			}
			call, cancel := context.WithTimeout(browser, b.timeout)
			target := chromedp.FromContext(browser).Target
			u, e := url.Parse(event.Request.URL)
			if e == nil && sameOrigin(base, u) && d.allowRequest(event.Request.Method, u.String()) {
				_, _ = cdp.Call(call, target, fetch.ContinueRequest, fetch.ContinueRequestParams{RequestID: event.RequestID})
			} else {
				_, _ = cdp.Call(call, target, fetch.FailRequest, fetch.FailRequestParams{RequestID: event.RequestID, ErrorReason: network.ErrorReasonBlockedByClient})
			}
			cancel()
		}
	}()
	err = chromedp.Do(startup, func(ctx context.Context, t *chromedp.Target) (chromedp.Void, error) {
		_, e := cdp.Call(ctx, t, fetch.Enable, fetch.EnableParams{})
		if e != nil {
			return chromedp.Void{}, e
		}
		_, e = cdp.Call(ctx, t, emulation.SetScriptExecutionDisabled, emulation.SetScriptExecutionDisabledParams{Value: true})
		return chromedp.Void{}, e
	})
	if err != nil {
		_ = d.close(context.Background())
		return nil, err
	}
	return b, nil
}

type profileLease struct{ path string }

func leaseProfile(path, agentID string) (*profileLease, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, errors.New("profile_path")
	}
	if err = os.MkdirAll(absolute, 0700); err != nil {
		return nil, errors.New("profile_create")
	}
	absolute, err = filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, errors.New("profile_path")
	}
	profiles.Lock()
	defer profiles.Unlock()
	if profiles.active[absolute] {
		return nil, errors.New("profile_busy")
	}
	marker := filepath.Join(absolute, ".threadwalker-agent")
	owner, createErr := os.OpenFile(marker, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if createErr == nil {
		_, writeErr := owner.WriteString(agentID)
		closeErr := owner.Close()
		if writeErr != nil || closeErr != nil {
			return nil, errors.New("profile_owner")
		}
	} else if !errors.Is(createErr, os.ErrExist) {
		return nil, errors.New("profile_owner")
	}
	data, readErr := os.ReadFile(marker)
	if readErr != nil || string(data) != agentID {
		return nil, errors.New("profile_owner")
	}
	profiles.active[absolute] = true
	return &profileLease{absolute}, nil
}
func (p *profileLease) close() { profiles.Lock(); delete(profiles.active, p.path); profiles.Unlock() }

type cdpDriver struct {
	stopLifetime               context.CancelFunc
	requestMu                  sync.Mutex
	submitURL                  string
	submitAllowed              bool
	ctx                        context.Context
	stopBrowser, stopAllocator context.CancelFunc
	release                    func()
	base                       *url.URL
	timeout                    time.Duration
	once                       sync.Once
	closeErr                   error
}

func (d *cdpDriver) call(ctx context.Context) (context.Context, func()) {
	call, cancel := context.WithTimeout(d.ctx, d.timeout)
	stop := context.AfterFunc(ctx, cancel)
	if ctx.Err() != nil {
		cancel()
	}
	if deadline, ok := ctx.Deadline(); ok {
		var previous = context.CancelFunc(cancel)
		call, cancel = context.WithDeadline(call, deadline)
		return call, func() { stop(); cancel(); previous() }
	}
	return call, func() { stop(); cancel() }
}
func (d *cdpDriver) navigate(ctx context.Context, dest string) error {
	call, cancel := d.call(ctx)
	defer cancel()
	return chromedp.Do(call, chromedp.Navigate(dest))
}
func (d *cdpDriver) snapshot(ctx context.Context) ([]byte, error) {
	call, cancel := d.call(ctx)
	defer cancel()
	origin, _ := json.Marshal(d.base.Scheme + "://" + d.base.Host)
	raw, err := chromedp.Run(call, chromedp.Evaluate[string](`(()=>{if(location.origin!==new URL(`+string(origin)+`).origin)return '{}';`+snapshotJS+`})()`))
	if err != nil {
		return nil, err
	}
	if len(raw) > 1<<20 {
		return nil, ErrMalformedPage
	}
	return []byte(raw), nil
}

// Only the single write armed immediately before requestSubmit can issue POST.
// Redirects that would resend the body cannot consume another allowance.
func (d *cdpDriver) allowRequest(method, raw string) bool {
	if method == "GET" || method == "HEAD" {
		return true
	}
	d.requestMu.Lock()
	defer d.requestMu.Unlock()
	if method != "POST" || !d.submitAllowed || raw != d.submitURL {
		return false
	}
	d.submitAllowed = false
	return true
}
func (d *cdpDriver) submit(ctx context.Context, text string) (bool, error) {
	call, cancel := d.call(ctx)
	defer cancel()
	raw, err := d.snapshot(call)
	if err != nil {
		return false, err
	}
	var state struct {
		Page string `json:"page"`
	}
	if json.Unmarshal(raw, &state) != nil || (state.Page != "compose" && state.Page != "post") {
		return false, nil
	}
	page, err := ParseSnapshot(raw, state.Page, d.base.String())
	if err != nil {
		return false, nil
	}
	if page.Form == nil {
		return false, nil
	}
	expected, err := url.Parse(page.Form.Action)
	if err != nil || !sameOrigin(d.base, expected) {
		return false, nil
	}
	d.requestMu.Lock()
	d.submitURL = expected.String()
	d.submitAllowed = true
	d.requestMu.Unlock()

	body, _ := json.Marshal(text)
	action, _ := json.Marshal(expected.String())
	attempted, err := chromedp.Run(call, chromedp.Evaluate[bool](`(()=>{const f=document.querySelector('form'),c=f?.querySelector('textarea[data-tw-compose]'),s=f?.querySelector('button[data-tw-submit]');if(!f||!c||!s||f.method!=='post'||f.action!==`+string(action)+`||f.target||s.formTarget||s.hasAttribute('formaction')||s.hasAttribute('formmethod')||s.disabled||c.disabled||c.name!=='text'||s.type!=='submit')return false;c.value=`+string(body)+`;f.requestSubmit(s);return true})()`))
	if err != nil || !attempted {
		d.disarmSubmit()
	}
	return attempted, err
}
func (d *cdpDriver) disarmSubmit() { d.requestMu.Lock(); d.submitAllowed = false; d.requestMu.Unlock() }
func (d *cdpDriver) acknowledgement(ctx context.Context) ([]byte, error) {
	defer d.disarmSubmit()
	call, cancel := d.call(ctx)
	defer cancel()
	if err := chromedp.Do(call, chromedp.WaitVisible(chromedp.CSS("[data-tw-thread-url], [data-tw-page='captcha'], [data-tw-page='logged_out']"))); err != nil {
		return nil, err
	}
	return d.snapshot(call)
}
func (d *cdpDriver) close(ctx context.Context) error {
	d.once.Do(func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(d.ctx), d.timeout)
		stop := context.AfterFunc(ctx, cancel)
		if ctx.Err() != nil {
			cancel()
		}
		if chromedp.FromContext(d.ctx).Browser != nil {
			d.closeErr = chromedp.Cancel(closeCtx)
		}
		stop()
		cancel()
		d.stopLifetime()
		d.stopBrowser()
		d.stopAllocator()
		d.release()
	})
	return d.closeErr
}

// The extractor deliberately accepts only the saved synthetic-v1 shape. It
// rejects duplicate/out-of-root markers instead of choosing the first match.
// Every string remains page data until ParseSnapshot validates the snapshot.
const snapshotJS = `
const roots=document.querySelectorAll('[data-tw-page]');
if(roots.length===0)return '{"contract":"synthetic-v1","page":"unknown"}';
if(roots.length!==1)return '{}';
const root=roots[0], all=(s)=>[...root.querySelectorAll(s)], one=(el,s)=>{const n=el.querySelectorAll(s);return n.length===1?n[0]:null};
if(document.querySelectorAll('[data-tw-contract]').length!==1||root.dataset.twContract!=='synthetic-v1'||document.querySelector('base'))return '{}';
for(const el of document.querySelectorAll('[data-tw-post],[data-tw-author],[data-tw-text],[data-tw-permalink],[data-tw-metric],[data-tw-compose],[data-tw-submit],[data-tw-thread-url],form'))if(!root.contains(el))return '{}';
let posts=[];for(const p of all('[data-tw-post]')){const a=one(p,'[data-tw-author]'),t=one(p,'[data-tw-text]'),u=one(p,'[data-tw-permalink]');if(!a||!t||!u||p.querySelector('[data-tw-post]'))return '{}';for(const k of ['data-tw-is-reply','data-tw-addressed-to-agent'])if(p.hasAttribute(k)&&!['true','false'].includes(p.getAttribute(k)))return '{}';posts.push({id:p.dataset.twPost,username:a.textContent,text:t.textContent,url:u.getAttribute('href'),is_reply:p.getAttribute('data-tw-is-reply')==='true',addressed_to_agent:p.getAttribute('data-tw-addressed-to-agent')==='true'});}
if(all('[data-tw-author]').length!==posts.length||all('[data-tw-text]').length!==posts.length||all('[data-tw-permalink]').length!==posts.length)return '{}';
let metrics={};for(const m of all('[data-tw-metric]')){const k=m.dataset.twMetric;if(Object.hasOwn(metrics,k))return '{}';metrics[k]=m.textContent;}
const forms=all('form'),composers=all('[data-tw-compose]'),submits=all('[data-tw-submit]'),links=all('[data-tw-thread-url]');
if(forms.length>1||composers.length>1||submits.length>1||links.length>1)return '{}';
let form=null;if(forms.length&&root.dataset.twPage==='search'){const f=forms[0];if(f.method!=='get'||new URL(f.action).origin!==location.origin||new URL(f.action).pathname!=='/search'||f.querySelectorAll('input[name=q]').length!==1||composers.length||submits.length)return '{}';}else if(forms.length){const f=forms[0],c=one(f,'textarea[data-tw-compose]'),s=one(f,'button[data-tw-submit]');if(!c||!s||f.elements.length!==2||f.enctype!=='application/x-www-form-urlencoded'||c.form!==f||s.form!==f||c.matches(':disabled')||s.matches(':disabled')||c.readOnly||s.name||c.name!=='text'||s.type!=='submit'||f.target||s.formTarget||s.hasAttribute('formaction')||s.hasAttribute('formmethod'))return '{}';form={action:f.getAttribute('action'),method:f.getAttribute('method')||'get',composer:true,submit:true};}else if(composers.length||submits.length)return '{}';
return JSON.stringify({contract:root.dataset.twContract,page:root.dataset.twPage,posts,metrics,thread_url:links.length?links[0].getAttribute('href'):'',form});
`
