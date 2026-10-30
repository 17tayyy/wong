//go:build js

// Command wongweb runs the wong dashboard inside a browser's service worker, on a sealed brain it decrypts there with a
// key of its own: the server only ever sees what it cannot read.
package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"syscall/js"
	"time"

	"github.com/17tayyy/wong/internal/dashboard"
	"github.com/17tayyy/wong/internal/remote"
	"github.com/17tayyy/wong/internal/seal"
)

// runtime is the brain this browser opened, or how far it got: locked, waiting for a machine to approve its key, open,
// or failed with a problem.
type runtime struct {
	mu      sync.Mutex
	state   string
	code    string
	problem string
	opened  *remote.Opened
	web     http.Handler
	session string
	log     *slog.Logger
}

func main() {
	rt := &runtime{state: "locked", log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	wong := js.Global().Get("Object").New()
	wong.Set("newDevice", js.FuncOf(func(js.Value, []js.Value) any {
		d, err := seal.NewDevice()
		if err != nil {
			return js.Null()
		}
		b, err := d.Bytes()
		if err != nil {
			return js.Null()
		}
		return toJS(b)
	}))
	wong.Set("start", js.FuncOf(rt.start))
	wong.Set("status", js.FuncOf(rt.status))
	wong.Set("serve", js.FuncOf(rt.serve))
	wong.Set("forget", js.FuncOf(rt.forget))
	js.Global().Set("wong", wong)
	select {}
}

// start opens the brain at origin with this browser's token and device key, waiting for a machine to approve the key
// the first time.
func (rt *runtime) start(_ js.Value, args []js.Value) any {
	if len(args) < 3 {
		return nil
	}
	origin, token, raw := args[0].String(), args[1].String(), fromJS(args[2])
	rt.mu.Lock()
	if rt.state != "locked" && rt.state != "failed" {
		rt.mu.Unlock()
		return nil
	}
	rt.state, rt.problem = "starting", ""
	rt.mu.Unlock()
	go func() {
		device, err := seal.DeviceFrom(raw)
		if err != nil {
			rt.fail(err)
			return
		}
		client := remote.NewClient(origin, token, 30*time.Second)
		client.Device = device
		ctx := context.Background()
		for {
			err := client.Unlock(ctx)
			if !errors.Is(err, remote.ErrLocked) {
				if err != nil {
					rt.fail(err)
					return
				}
				break
			}
			rt.mu.Lock()
			rt.state, rt.code = "waiting", seal.Fingerprint(device.Public())
			rt.mu.Unlock()
			time.Sleep(3 * time.Second)
		}
		opened, err := remote.OpenThrough(ctx, client)
		if err != nil {
			rt.fail(err)
			return
		}
		session, err := opened.Plain.NewSession("you")
		if err != nil {
			rt.fail(err)
			return
		}
		web, err := dashboard.Handler(opened.Plain, rt.log, dashboard.Options{Devices: remote.NewDevices(), SignIns: remote.NewSignIns(), Keyring: remote.Holder{C: client}})
		if err != nil {
			rt.fail(err)
			return
		}
		rt.mu.Lock()
		rt.state, rt.opened, rt.web, rt.session = "open", opened, web, session
		rt.mu.Unlock()
	}()
	return nil
}

func (rt *runtime) fail(err error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.state, rt.problem = "failed", err.Error()
}

func (rt *runtime) status(js.Value, []js.Value) any {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return map[string]any{"state": rt.state, "code": rt.code, "problem": rt.problem}
}

// forget drops the brain from memory, for a browser that signs out.
func (rt *runtime) forget(js.Value, []js.Value) any {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.opened != nil {
		rt.opened.Close()
	}
	rt.state, rt.code, rt.problem, rt.opened, rt.web = "locked", "", "", nil, nil
	return nil
}

// serve answers a request of the page with the dashboard on the brain in memory: it brings in what the machines pushed
// first, and sends what the request changed after, encrypted. It returns a promise of {status, headers, body}.
func (rt *runtime) serve(_ js.Value, args []js.Value) any {
	method, url, headers := args[0].String(), args[1].String(), args[2]
	var body []byte
	if len(args) > 3 && !args[3].IsNull() && !args[3].IsUndefined() {
		body = fromJS(args[3])
	}
	return promise(func() (any, error) {
		rt.mu.Lock()
		defer rt.mu.Unlock()
		if rt.state != "open" {
			return nil, errors.New("the brain is not open in this browser")
		}
		req, err := http.NewRequest(method, url, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		for i := 0; i < headers.Length(); i++ {
			pair := headers.Index(i)
			req.Header.Add(pair.Index(0).String(), pair.Index(1).String())
		}
		req.Header.Set("Cookie", "wong_token="+rt.session)
		ctx := context.Background()
		if err := rt.opened.Refresh(ctx); err != nil {
			rt.log.Warn("refresh", "err", err)
		}
		rec := httptest.NewRecorder()
		rt.web.ServeHTTP(rec, req)
		if err := rt.opened.Flush(ctx, "you"); err != nil {
			rt.log.Warn("flush", "err", err)
		}
		out := js.Global().Get("Array").New()
		for k, vs := range rec.Header() {
			for _, v := range vs {
				out.Call("push", []any{k, v})
			}
		}
		return map[string]any{"status": rec.Code, "headers": out, "body": toJS(rec.Body.Bytes())}, nil
	})
}

// promise runs do in a goroutine, as anything that waits on the network must in a browser, and settles a promise.
func promise(do func() (any, error)) js.Value {
	return js.Global().Get("Promise").New(js.FuncOf(func(_ js.Value, args []js.Value) any {
		resolve, reject := args[0], args[1]
		go func() {
			v, err := do()
			if err != nil {
				reject.Invoke(js.Global().Get("Error").New(err.Error()))
				return
			}
			resolve.Invoke(v)
		}()
		return nil
	}))
}

func toJS(b []byte) js.Value {
	a := js.Global().Get("Uint8Array").New(len(b))
	js.CopyBytesToJS(a, b)
	return a
}

func fromJS(v js.Value) []byte {
	b := make([]byte, v.Get("length").Int())
	js.CopyBytesToGo(b, v)
	return b
}
