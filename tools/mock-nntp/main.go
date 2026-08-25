// mock-nntp is a development stand-in for a Usenet provider, speaking just
// enough NNTP to exercise the provider health UI: greeting, AUTHINFO, QUIT.
//
// Its point is the runtime toggle. Save-time validation refuses a provider
// whose credentials fail, so a broken provider cannot be *saved* into the app —
// the states worth testing are the ones that happen to a provider that was
// healthy when it was added. Start this server, add it as a provider (the save
// validates against it and passes), then press Enter in this console to cycle
// what the server does to the next connection:
//
//	accept     valid credentials are accepted (the healthy baseline)
//	reject     every AUTHINFO PASS answers 481 — the "password changed /
//	           subscription ended" case; the provider should go blocked
//	conn-limit the greeting itself answers 502 — the "account is out of
//	           connections" case; the provider should degrade, not block
//
// Trigger a check from the app between toggles ("Check again" on the provider
// card, or any playback), and watch the dashboard update live.
//
// Usage:
//
//	go run ./tools/mock-nntp [-port 1190] [-user test] [-pass test]
//
// Plain TCP only — configure the provider with SSL off.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

const (
	modeAccept = iota
	modeReject
	modeConnLimit
	modeCount
)

var modeNames = [modeCount]string{"accept", "reject", "conn-limit"}

func main() {
	port := flag.Int("port", 1190, "port to listen on")
	user := flag.String("user", "test", "username accepted in accept mode")
	pass := flag.String("pass", "test", "password accepted in accept mode")
	flag.Parse()

	var mode atomic.Int32

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port))
	if err != nil {
		fmt.Fprintf(os.Stderr, "listen: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("mock-nntp listening on %s (user=%q pass=%q)\n", ln.Addr(), *user, *pass)
	fmt.Printf("mode: %s — press Enter to cycle accept -> reject -> conn-limit\n", modeNames[mode.Load()])

	// The toggle: each Enter advances the mode for every subsequent connection.
	go func() {
		stdin := bufio.NewScanner(os.Stdin)
		for stdin.Scan() {
			next := (mode.Load() + 1) % modeCount
			mode.Store(next)
			fmt.Printf("mode: %s\n", modeNames[next])
		}
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			fmt.Fprintf(os.Stderr, "accept: %v\n", err)
			return
		}
		go serve(conn, &mode, *user, *pass)
	}
}

func serve(conn net.Conn, mode *atomic.Int32, validUser, validPass string) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Minute))
	remote := conn.RemoteAddr().String()
	w := bufio.NewWriter(conn)
	say := func(format string, args ...any) {
		fmt.Fprintf(w, format+"\r\n", args...)
		_ = w.Flush()
	}

	if mode.Load() == modeConnLimit {
		fmt.Printf("[%s] refused: 502 connection limit\n", remote)
		say("502 too many connections for your account")
		return
	}
	say("200 mock-nntp ready")

	var gotUser string
	var authed bool
	r := bufio.NewScanner(conn)
	for r.Scan() {
		line := strings.TrimSpace(r.Text())
		verb := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(verb, "AUTHINFO USER "):
			gotUser = line[len("AUTHINFO USER "):]
			say("381 password required")

		case strings.HasPrefix(verb, "AUTHINFO PASS "):
			gotPass := line[len("AUTHINFO PASS "):]
			switch {
			case mode.Load() == modeReject:
				fmt.Printf("[%s] rejected auth (mode reject) user=%q\n", remote, gotUser)
				say("481 authentication failed")
			case gotUser == validUser && gotPass == validPass:
				authed = true
				fmt.Printf("[%s] authenticated user=%q\n", remote, gotUser)
				say("281 welcome")
			default:
				fmt.Printf("[%s] rejected auth (bad credentials) user=%q\n", remote, gotUser)
				say("481 authentication failed")
			}

		case verb == "QUIT":
			say("205 bye")
			return

		case verb == "DATE":
			say("111 %s", time.Now().UTC().Format("20060102150405"))

		case !authed:
			say("480 authentication required")

		default:
			// Enough for health testing; anything real is out of scope.
			say("500 command not supported by mock-nntp")
		}
	}
}
