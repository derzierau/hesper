package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
)

// runAttach implements `hesperd attach <id> [--ro] [--owner]` as the contract
// describes: bridge this process's tty to an attach connection.
func runAttach(args []string) int {
	fs := flag.NewFlagSet("attach", flag.ContinueOnError)
	ro := fs.Bool("ro", false, "read-only")
	owner := fs.Bool("owner", false, "become the size owner")
	view := fs.Bool("view", false, "read-only window onto the last rows (follows this terminal's size)")
	viewRows := fs.Int("view-rows", 0, "rows of the view (implies --view)")
	fit := fs.Bool("fit", false, "a view that also asks the PTY for this terminal's size (implies --view; the fake ignores the fit)")
	sock := fs.String("socket", "", "socket path")
	// Allow flags after the id.
	var id string
	rest := args
	for len(rest) > 0 {
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		rest = fs.Args()
		if len(rest) > 0 {
			id = rest[0]
			rest = rest[1:]
		}
	}
	if id == "" {
		fmt.Fprintln(os.Stderr, "usage: attach <id> [--ro] [--owner]")
		return 2
	}
	path, err := socketPath(*sock)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	conn, err := net.Dial("unix", path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\r\nhesperd: cannot connect: %v\r\n", err)
		return 1
	}
	defer conn.Close()

	in := int(os.Stdin.Fd())
	cols, rows, err := getWinsize(in)
	if err != nil || cols == 0 {
		cols, rows = 80, 24
	}
	mode := "rw"
	if *ro {
		mode = "ro"
	}
	isView := *view || *viewRows > 0 || *fit
	req := map[string]any{"attach": id, "mode": mode, "cols": cols, "rows": rows, "owner": *owner && !*ro}
	if isView {
		*ro = true
		r := *viewRows
		if r <= 0 {
			r = rows
		}
		req["mode"], req["owner"] = "ro", false
		req["view"] = map[string]any{"rows": r, "cols": cols, "anchor": "bottom"}
		if *fit {
			req["fit"] = map[string]any{"cols": cols, "rows": rows}
		}
	}
	hello, _ := json.Marshal(req)
	if _, err := conn.Write(append(hello, '\n')); err != nil {
		return 1
	}
	br := bufio.NewReaderSize(conn, 1<<16)
	line, err := br.ReadBytes('\n')
	if err != nil {
		fmt.Fprintf(os.Stderr, "\r\nhesperd: attach failed: %v\r\n", err)
		return 1
	}
	var reply struct {
		OK    bool `json:"ok"`
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(line, &reply) != nil || !reply.OK {
		fmt.Fprintf(os.Stderr, "\r\nhesperd: attach refused: %s\r\n", reply.Error.Message)
		return 1
	}

	restore, _ := makeRaw(in)
	defer restore()

	var wmu = make(chan struct{}, 1)
	send := func(typ byte, p []byte) {
		wmu <- struct{}{}
		_ = writeFrame(conn, typ, p)
		<-wmu
	}

	go func() {
		buf := make([]byte, 32<<10)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil {
				return
			}
			if !*ro {
				p := make([]byte, n)
				copy(p, buf[:n])
				send(frameData, p)
			}
		}
	}()
	if (*owner && !*ro) || isView {
		winch := make(chan os.Signal, 4)
		signal.Notify(winch, syscall.SIGWINCH)
		go func() {
			for range winch {
				if c, r, err := getWinsize(in); err == nil {
					send(frameResize, sizePayload(c, r))
				}
			}
		}()
	}

	out := os.Stdout
	for {
		typ, p, err := readFrame(br)
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				restore()
				fmt.Fprint(out, "\r\n\x1b[0m[hesperd: connection closed]\r\n")
			}
			return 1
		}
		switch typ {
		case frameData:
			if _, err := out.Write(p); err != nil {
				return 1
			}
		case frameSize:
			// The app learns the new size from agents.changed; nothing to do.
		case frameExit:
			// Part D: exit 0 on EXIT.
			return 0
		}
	}
}
