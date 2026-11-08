package transport_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// echoServer listens on the loopback (M's, in the world) and echoes.
func echoServer(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				io.Copy(c, c)
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func addForward(t *testing.T, w *world, port int) wire.Forward {
	t.Helper()
	w.L.waitLinked(t, "M")
	var fw wire.Forward
	if err := w.L.call(t, "forwards.add", wire.ForwardAddParams{Machine: "M", Port: port}, &fw); err != nil {
		t.Fatal(err)
	}
	if fw.Local == 0 || fw.ID != strconv.Itoa(fw.Local) {
		t.Fatalf("forward %+v", fw)
	}
	return fw
}

func dial(t *testing.T, port int) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// echo sends data through c and checks it comes back whole.
func echo(t *testing.T, c net.Conn, data []byte) {
	t.Helper()
	c.SetDeadline(time.Now().Add(60 * time.Second))
	defer c.SetDeadline(time.Time{})
	sent := make(chan error, 1)
	go func() {
		_, err := c.Write(data)
		sent <- err
	}()
	got := make([]byte, len(data))
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatalf("echo: %v", err)
	}
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("the echo differs")
	}
}

// refused: the forwarded connection ends without a byte.
func refused(t *testing.T, c net.Conn) {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(30 * time.Second))
	if n, err := c.Read(make([]byte, 1)); n > 0 || err == nil || isTimeout(err) {
		t.Fatalf("not refused: %d %v", n, err)
	}
}

func isTimeout(err error) bool {
	ne, ok := err.(net.Error)
	return ok && ne.Timeout()
}

func forwardError(t *testing.T, n *node, id string) string {
	t.Helper()
	var list []wire.Forward
	if err := n.call(t, "forwards.list", nil, &list); err != nil {
		t.Fatal(err)
	}
	for _, fw := range list {
		if fw.ID == id {
			return fw.Error
		}
	}
	t.Fatalf("no forward %s in %+v", id, list)
	return ""
}

// L forwards a local port to an echo server on M's loopback: 10 MB each
// way arrive whole (the window holds the link's readers to no waiting),
// the relay sees none of it, and the forward lists its connection.
func TestRemoteForwardEchoes(t *testing.T) {
	w := newWorld(t, worldOptions{forward: "all"})
	port := echoServer(t)
	fw := addForward(t, w, port)
	if fw.Machine != "M" || fw.Port != port {
		t.Fatalf("forward %+v", fw)
	}
	c := dial(t, fw.Local)
	data := bytes.Repeat([]byte("FORWARDED-PLAINTEXT-7731 "), (10<<20)/25)
	echo(t, c, data)
	if leaked := w.tap.sawAny("FORWARDED-PLAINTEXT-7731"); leaked != "" {
		t.Fatalf("the relay saw %q", leaked)
	}
	var list []wire.Forward
	w.L.call(t, "forwards.list", nil, &list)
	if len(list) != 1 || list[0].Conns != 1 || list[0].Error != "" {
		t.Fatalf("list %+v", list)
	}
	// Removing it ends the connection and the listener.
	if err := w.L.call(t, "forwards.remove", wire.IDParams{ID: "M:" + strconv.Itoa(port)}, nil); err != nil {
		t.Fatal(err)
	}
	refused(t, c)
	if _, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(fw.Local))); err == nil {
		t.Fatal("still listening")
	}
}

// Forwards persist: a restarted L listens again.
func TestRemoteForwardPersists(t *testing.T) {
	w := newWorld(t, worldOptions{forward: "all"})
	port := echoServer(t)
	fw := addForward(t, w, port)
	w.L.stop()
	if saved, _ := os.ReadFile(filepath.Join(w.L.keys, "forwards.json")); !strings.Contains(string(saved), `"local": `+fw.ID) {
		t.Fatal("not saved")
	}
	w.L.start(t, w.ctx)
	w.L.waitLinked(t, "M")
	echo(t, dial(t, fw.Local), []byte("again"))
}

// The refusals: a host without --allow-forward (refused before anything
// is sent to it), a port not in its list, a device without the shell
// right; and an agent may not add a forward.
func TestRemoteForwardRefusals(t *testing.T) {
	port := echoServer(t)
	w := newWorld(t, worldOptions{})
	w.L.waitLinked(t, "M")
	if err := w.L.call(t, "forwards.add", wire.ForwardAddParams{Machine: "M", Port: port}, nil); wireCode(err) != wire.CodeUnavailable ||
		!strings.Contains(err.Error(), "--allow-forward") {
		t.Fatalf("a host without --allow-forward: %v", err)
	}
	var agent wire.Agent
	if err := w.L.call(t, "agents.spawn", wire.SpawnParams{Project: w.L.project, Task: "small job"}, &agent); err != nil {
		t.Fatal(err)
	}
	as := w.L.client(t)
	as.Caller = agent.ID
	ctx, cancel := context.WithTimeout(w.ctx, 30*time.Second)
	defer cancel()
	var we *wire.Error
	if err := as.Call(ctx, "forwards.add", wire.ForwardAddParams{Machine: "M", Port: port}, nil); !errors.As(err, &we) || we.Code != wire.CodeForbidden {
		t.Fatalf("an agent added a forward: %v", err)
	}
	if err := as.Call(ctx, "forwards.list", nil, &[]wire.Forward{}); err != nil {
		t.Fatalf("an agent may list forwards: %v", err)
	}

	w = newWorld(t, worldOptions{forward: "1"})
	fw := addForward(t, w, port)
	refused(t, dial(t, fw.Local))
	if e := forwardError(t, w.L, fw.ID); !strings.Contains(e, "not among the ports") {
		t.Fatalf("error %q", e)
	}

	w = newWorld(t, worldOptions{forward: "all", rights: []string{"observe", "answer", "type", "transfer"}})
	fw = addForward(t, w, port)
	refused(t, dial(t, fw.Local))
	if e := forwardError(t, w.L, fw.ID); e == "" {
		t.Fatal("no error without the shell right")
	}
}

// A host without the capability is never sent ports.open or W: a saved
// forward to it is refused on L, and the link stays up.
func TestRemoteForwardSkipsHostsWithoutIt(t *testing.T) {
	port := echoServer(t)
	w := newWorld(t, worldOptions{beforeStart: func(L, M *node) {
		writeJSON(t, filepath.Join(L.keys, "forwards.json"), map[string]any{"forwards": []wire.Forward{{Machine: "M", Port: port, Local: freeLocal(t)}}})
	}})
	w.L.waitLinked(t, "M")
	var list []wire.Forward
	w.L.call(t, "forwards.list", nil, &list)
	if len(list) != 1 {
		t.Fatalf("list %+v", list)
	}
	links := w.M.d.Service.Links()
	refused(t, dial(t, list[0].Local))
	if e := forwardError(t, w.L, list[0].ID); !strings.Contains(e, "does not forward ports") {
		t.Fatalf("error %q", e)
	}
	time.Sleep(300 * time.Millisecond)
	if w.L.d.Fleet.Route("M") == "" || w.M.d.Service.Links() != links {
		t.Fatal("the link went")
	}
}

// On the direct path the forward's bytes bypass the relay.
func TestRemoteForwardDirect(t *testing.T) {
	w := newWorld(t, worldOptions{direct: true, forward: "all"})
	port := echoServer(t)
	deadline := time.Now().Add(15 * time.Second)
	for w.L.d.Fleet.Route("M") != "direct" {
		if time.Now().After(deadline) {
			t.Fatalf("the link stayed on %q", w.L.d.Fleet.Route("M"))
		}
		time.Sleep(20 * time.Millisecond)
	}
	fw := addForward(t, w, port)
	streams := w.tap.streams.Load()
	data := make([]byte, 4<<20)
	rand.Read(data)
	echo(t, dial(t, fw.Local), append([]byte("OVER-DIRECT-FORWARD"), data...))
	if w.tap.sawAny("OVER-DIRECT-FORWARD") != "" || w.tap.streams.Load() != streams {
		t.Fatal("the forward went through the relay")
	}
}

// A link that breaks resets its forwarded connections; new ones use the
// next link.
func TestRemoteForwardLinkRestart(t *testing.T) {
	w := newWorld(t, worldOptions{forward: "all"})
	port := echoServer(t)
	fw := addForward(t, w, port)
	c := dial(t, fw.Local)
	echo(t, c, []byte("before"))
	w.tap.dropAll()
	refused(t, c)
	time.Sleep(200 * time.Millisecond)
	w.L.waitLinked(t, "M")
	deadline := time.Now().Add(20 * time.Second)
	for {
		c := dial(t, fw.Local)
		c.SetDeadline(time.Now().Add(5 * time.Second))
		c.Write([]byte("after"))
		got := make([]byte, 5)
		if _, err := io.ReadFull(c, got); err == nil && string(got) == "after" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("no forward after the link came back")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// A bulk transfer on the link leaves a terminal on the same link
// responsive.
func TestRemoteForwardKeepsTerminalsResponsive(t *testing.T) {
	w := newWorld(t, worldOptions{shell: true, forward: "all"})
	port := echoServer(t)
	a := spawnShell(t, w)
	rw := attach(t, w.L, wire.AttachRequest{Attach: a.ID, Mode: wire.ModeRW})
	readUntil(t, rw, "$")
	fw := addForward(t, w, port)
	c := dial(t, fw.Local)
	done := make(chan struct{})
	go func() {
		defer close(done)
		data := make([]byte, 30<<20)
		go c.Write(data)
		io.ReadFull(c, make([]byte, len(data)))
	}()
	time.Sleep(200 * time.Millisecond)
	started := time.Now()
	rw.Input([]byte("DURING-BULK\r"))
	readUntil(t, rw, "ran DURING-BULK")
	if took := time.Since(started); took > 3*time.Second {
		t.Fatalf("typing took %v during the transfer", took)
	}
	select {
	case <-done:
		t.Log("the transfer ended before the typing came back")
	default:
	}
	c.Close()
	<-done
}

// Removing a forward closes its connections on the host too: the
// service's side of each ends.
func TestRemoteForwardRemoveClosesHostSide(t *testing.T) {
	w := newWorld(t, worldOptions{forward: "all"})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	ended := make(chan struct{}, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		io.Copy(c, c)
		c.Close()
		ended <- struct{}{}
	}()
	fw := addForward(t, w, ln.Addr().(*net.TCPAddr).Port)
	echo(t, dial(t, fw.Local), []byte("hello"))
	if err := w.L.call(t, "forwards.remove", wire.IDParams{ID: fw.ID}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the host kept the service's connection open")
	}
}

func freeLocal(t *testing.T) int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}
