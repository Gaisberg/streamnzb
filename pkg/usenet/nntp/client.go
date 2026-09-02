package nntp

import (
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
	"time"

	"streamnzb/pkg/core/logger"
)

const dialTimeout = 30 * time.Second

// bodyReadIdleTimeout is a rolling deadline for article body reads: it is
// refreshed on every Read, so it bounds how long a body read may sit without
// receiving any bytes — not the total body transfer time. A healthy provider
// streams continuously; a connection that goes silent mid-body (silently
// dropped by NAT/firewall, provider-side kill) previously blocked the fetch
// for the full 5-minute absolute deadline, stalling playback on that segment.
const bodyReadIdleTimeout = 15 * time.Second

type Client struct {
	conn    *textproto.Conn
	netConn net.Conn
	host    string
	port    int
	ssl     bool
	user    string
	pass    string

	// group is the newsgroup currently selected on this connection, so a
	// repeat GROUP for the same group can be skipped.
	group string

	LastUsed time.Time
	pool     *ClientPool

	// budget holds the account permit this connection was dialed under, and
	// generation names the pool configuration it was dialed for. Both are set
	// by the pool at dial time and read back when the connection is closed or
	// returned, so a permit always goes back to the budget it came from even
	// after the pool has been re-pointed at another account.
	budget     *connBudget
	generation uint64

	// lifecycle guards conn against the one write that happens off the
	// command goroutine: Quit, which a watchdog may call mid-command to abort
	// a read. closed is sticky. Reconnect checks it before and after dialing,
	// because a reconnect that lands after Quit would open a connection nothing
	// owns — the pool has already freed the slot and counted the old one gone,
	// and the new one would sit on the provider's account until it timed out.
	lifecycle sync.Mutex
	closed    bool
}

// ErrClientClosed is returned by commands on a connection that has been closed
// by its pool.
var ErrClientClosed = errors.New("nntp: client closed")

func readGreeting(tp *textproto.Conn) error {
	code, _, err := tp.ReadResponse(200)
	if err == nil {
		return nil
	}
	var protoErr *textproto.Error
	if errors.As(err, &protoErr) && protoErr.Code == 201 && code == 201 {
		return nil
	}
	return err
}

// dialNNTP opens a TCP or TLS connection and reads the greeting. The greeting
// is read under a deadline so a server that accepts and then says nothing
// cannot hold the dial open indefinitely.
func dialNNTP(address string, port int, ssl bool) (net.Conn, *textproto.Conn, error) {
	fullAddr := net.JoinHostPort(address, strconv.Itoa(port))
	var conn net.Conn
	var err error

	if ssl {
		dialer := &net.Dialer{Timeout: dialTimeout}
		conn, err = tls.DialWithDialer(dialer, "tcp", fullAddr, nil)
	} else {
		conn, err = net.DialTimeout("tcp", fullAddr, dialTimeout)
	}
	if err != nil {
		return nil, nil, err
	}

	logger.VerboseNNTP("nntp NewClient connection opened", "addr", fullAddr)
	conn.SetDeadline(time.Now().Add(30 * time.Second))
	tp := textproto.NewConn(conn)
	if err = readGreeting(tp); err != nil {
		tp.Close()
		return nil, nil, err
	}
	conn.SetDeadline(time.Time{})
	return conn, tp, nil
}

func NewClient(address string, port int, ssl bool) (*Client, error) {
	conn, tp, err := dialNNTP(address, port, ssl)
	if err != nil {
		return nil, err
	}
	return &Client{
		conn:    tp,
		netConn: conn,
		host:    address,
		port:    port,
		ssl:     ssl,
	}, nil
}

func (c *Client) SetPool(p *ClientPool) {
	c.pool = p
}

func (c *Client) Authenticate(user, pass string) error {
	c.user = user
	c.pass = pass
	c.setDeadline()
	id, err := c.conn.Cmd("AUTHINFO USER %s", user)
	if err != nil {
		return err
	}
	c.conn.StartResponse(id)
	code, _, err := c.conn.ReadCodeLine(381)
	c.conn.EndResponse(id)

	if err != nil {

		if code == 281 {
			return nil
		}
		return err
	}

	id, err = c.conn.Cmd("AUTHINFO PASS %s", pass)
	if err != nil {
		return err
	}
	c.conn.StartResponse(id)
	_, _, err = c.conn.ReadCodeLine(281)
	c.conn.EndResponse(id)
	return err
}

// Group selects a newsgroup, unless this connection already has it selected.
//
// Every segment fetch used to send GROUP before BODY, so each article cost two
// command round trips instead of one — on a leased connection that, in the
// overwhelming majority of cases, had that exact group selected already. The
// selection is per-connection state that only Reconnect can invalidate, so
// caching it is safe: a connection that has silently died fails on the next
// BODY exactly as it would have failed on the redundant GROUP.
func (c *Client) Group(group string) error {
	const maxRetries = 2

	if c.group == group {
		return nil
	}

	for i := 0; i <= maxRetries; i++ {
		c.setDeadline()
		id, err := c.conn.Cmd("GROUP %s", group)
		if err != nil {
			if c.shouldRetry(0, err) {
				if recErr := c.Reconnect(); recErr == nil {
					continue
				}
			}
			return err
		}

		c.conn.StartResponse(id)
		code, _, err := c.conn.ReadCodeLine(211)
		c.conn.EndResponse(id)

		if err == nil {
			c.group = group
			return nil
		}

		if c.shouldRetry(code, err) {
			if recErr := c.Reconnect(); recErr == nil {
				continue
			}
		} else {
			return err
		}
	}

	return errors.New("group command failed after retries")
}

type bodyReader struct {
	io.Reader
	endResponse func()
	once        sync.Once
}

func (b *bodyReader) Read(p []byte) (n int, err error) {
	n, err = b.Reader.Read(p)
	if err == io.EOF {
		b.once.Do(b.endResponse)
	}
	return n, err
}

// Close ensures EndResponse is called even when the caller stops reading
// before reaching io.EOF (e.g. on a decode error or context cancellation).
// It is idempotent; calling it after a complete read is a safe no-op.
func (b *bodyReader) Close() error {
	b.once.Do(b.endResponse)
	return nil
}

func formatMessageID(messageID string) string {
	s := strings.TrimSpace(messageID)
	if len(s) >= 2 && s[0] == '<' && s[len(s)-1] == '>' {
		return s
	}
	return "<" + s + ">"
}

// Body returns the body of the article identified by messageID.
// The caller MUST close the returned ReadCloser when done (or on error)
// to release the underlying textproto pipeline slot.
func (c *Client) Body(messageID string) (io.ReadCloser, error) {
	const maxRetries = 2
	var lastErr error

	for i := 0; i <= maxRetries; i++ {

		c.setDeadline()
		bodyArg := formatMessageID(messageID)
		id, err := c.conn.Cmd("BODY %s", bodyArg)
		if err != nil {

			lastErr = err

			if c.shouldRetry(0, err) {
				if recErr := c.Reconnect(); recErr == nil {
					continue
				}
			}
			return nil, err
		}

		c.conn.StartResponse(id)
		code, _, err := c.conn.ReadCodeLine(222)
		if err != nil {
			c.conn.EndResponse(id)
			lastErr = err
			if c.shouldRetry(code, err) {
				if recErr := c.Reconnect(); recErr == nil {
					continue
				}
			}
			return nil, err
		}

		// Body reads use the rolling idle deadline set by metricReader; the
		// fetch context's overall timeout bounds the total transfer.
		// articleReader, not DotReader: the body is handed to the caller in
		// canonical wire form (CRLF, still dot-stuffed) in a single pass. See
		// articlereader.go for why un-stuffing here would corrupt yEnc data.
		metricR := &metricReader{r: newArticleReader(c.conn.R), client: c}

		return &bodyReader{
			Reader:      metricR,
			endResponse: func() { c.conn.EndResponse(id) },
		}, nil
	}
	return nil, lastErr
}

type metricReader struct {
	r      io.Reader
	client *Client
}

func (m *metricReader) Read(p []byte) (n int, err error) {
	if m.client.netConn != nil {
		m.client.netConn.SetReadDeadline(time.Now().Add(bodyReadIdleTimeout))
	}
	n, err = m.r.Read(p)
	if n > 0 && m.client.pool != nil {
		m.client.pool.TrackRead(n)
	}
	return n, err
}

func (c *Client) shouldRetry(code int, err error) bool {

	if code == 480 {
		return true
	}

	if code == 0 && err != nil {
		return true
	}
	return false
}

// Reconnect replaces a dead socket with a fresh one to the same server, keeping
// the account permit and the pool's counters exactly as they were: to the pool
// this is still the same connection.
//
// It refuses once the pool has closed the client. The command that noticed the
// socket was gone cannot tell a provider-side drop from a watchdog's Quit, and
// retrying the latter used to redial an orphan the provider counted against
// the account for as long as it cared to keep it.
func (c *Client) Reconnect() error {
	c.lifecycle.Lock()
	if c.closed {
		c.lifecycle.Unlock()
		return ErrClientClosed
	}
	old := c.conn
	c.lifecycle.Unlock()
	if old != nil {
		old.Close()
	}
	// A fresh connection has no group selected.
	c.group = ""

	conn, tp, err := dialNNTP(c.host, c.port, c.ssl)
	if err != nil {
		if c.pool != nil && IsConnectionLimit(err) {
			c.pool.reportConnLimit(err)
		}
		return err
	}

	c.lifecycle.Lock()
	if c.closed {
		c.lifecycle.Unlock()
		tp.Close()
		return ErrClientClosed
	}
	c.conn = tp
	c.netConn = conn
	c.lifecycle.Unlock()

	if c.user == "" {
		return nil
	}
	err = c.Authenticate(c.user, c.pass)
	// The verdict is about this connection's credentials, which are only the
	// provider's current ones if the pool has not been re-pointed since.
	if c.pool != nil && c.pool.dialsAs(c) {
		c.pool.reportAuthResult(err)
	}
	return err
}

// Quit closes the socket. It is idempotent, and it reports whether this call
// was the one that closed it, so the pool's counters move exactly once however
// many paths end up tearing the same connection down.
func (c *Client) Quit() error {
	_, err := c.close()
	return err
}

func (c *Client) close() (first bool, err error) {
	c.lifecycle.Lock()
	defer c.lifecycle.Unlock()
	if c.closed {
		return false, nil
	}
	c.closed = true
	addr := net.JoinHostPort(c.host, strconv.Itoa(c.port))
	logger.VerboseNNTP("nntp Client Quit closing connection", "addr", addr)
	if c.conn == nil {
		return true, nil
	}
	return true, c.conn.Close()
}

func (c *Client) isClosed() bool {
	c.lifecycle.Lock()
	defer c.lifecycle.Unlock()
	return c.closed
}

func (c *Client) setDeadline() {
	if c.netConn != nil {
		c.netConn.SetDeadline(time.Now().Add(60 * time.Second))
	}
}

func (c *Client) setShortDeadline() {
	if c.netConn != nil {

		c.netConn.SetDeadline(time.Now().Add(2 * time.Second))
	}
}

// HealthyForCheckout reports whether a pooled connection is safe to hand out.
// Connections idle longer than maxIdle are liveness-checked with a DATE command
// under a short (2s) deadline: providers silently drop idle TCP sessions, and
// handing out a dead connection makes the next command hang for its full 60s
// deadline — times retries — before failing, turning one stale socket into a
// multi-minute playback stall.
func (c *Client) HealthyForCheckout(maxIdle time.Duration) bool {
	if c == nil || c.conn == nil || c.isClosed() {
		return false
	}
	if !c.LastUsed.IsZero() && time.Since(c.LastUsed) <= maxIdle {
		return true
	}
	return c.Ping() == nil
}

// Ping issues DATE and waits for the reply under a short (2s) deadline. It is
// the cheapest full round-trip on an established connection, so timing it
// measures server responsiveness alone — no TCP/TLS handshake, greeting or auth
// folded in.
func (c *Client) Ping() error {
	if c == nil || c.conn == nil {
		return errors.New("nntp: ping on closed client")
	}
	c.setShortDeadline()
	id, err := c.conn.Cmd("DATE")
	if err != nil {
		return err
	}
	c.conn.StartResponse(id)
	_, _, err = c.conn.ReadCodeLine(111)
	c.conn.EndResponse(id)
	return err
}

// readMultiLine consumes a dot-terminated multi-line response via DotReader,
// which also un-stuffs leading ".." octets. The manual ReadLine loop this
// replaces returned dot-stuffed lines verbatim; the proxy then stuffed them
// AGAIN on the way out, corrupting any body line starting with a dot.
// The result carries no trailing newline, matching the previous contract.
func (c *Client) readMultiLine() (string, error) {
	data, err := io.ReadAll(c.conn.DotReader())
	if err != nil {
		return "", err
	}
	result := strings.TrimSuffix(string(data), "\n")
	if c.pool != nil {
		c.pool.TrackRead(len(result))
	}
	return result, nil
}

// readCommand issues verb for messageID, expects the given success code and
// returns the dot-terminated body as a string.
func (c *Client) readCommand(verb string, code int, messageID string) (string, error) {
	c.setDeadline()
	id, err := c.conn.Cmd(verb+" %s", formatMessageID(messageID))
	if err != nil {
		return "", err
	}

	c.conn.StartResponse(id)
	defer c.conn.EndResponse(id)

	if _, _, err = c.conn.ReadCodeLine(code); err != nil {
		return "", err
	}
	return c.readMultiLine()
}

func (c *Client) GetArticle(messageID string) (string, error) {
	return c.readCommand("ARTICLE", 220, messageID)
}

// drainBackendBody discards any unread body remaining in the pipeline.
// It is capped at 10 MB to prevent a corrupt or adversarial response from
// stalling the connection indefinitely.
func (c *Client) drainBackendBody() {
	const maxDrainBytes = 10 << 20 // 10 MB
	n, _ := io.Copy(io.Discard, io.LimitReader(c.conn.DotReader(), maxDrainBytes))
	if c.pool != nil && n > 0 {
		c.pool.TrackRead(int(n))
	}
}

func (c *Client) StreamBody(messageID string, w io.Writer) (written int64, err error) {
	c.setDeadline()
	id, err := c.conn.Cmd("BODY %s", formatMessageID(messageID))
	if err != nil {
		return 0, err
	}

	c.conn.StartResponse(id)
	defer func() {
		c.conn.EndResponse(id)
		if err != nil {
			c.drainBackendBody()
		}
	}()

	_, _, err = c.conn.ReadCodeLine(222)
	if err != nil {
		return 0, err
	}

	header := "222 0 " + messageID + "\r\n"
	n, err := w.Write([]byte(header))
	written += int64(n)
	if err != nil {
		return written, err
	}

	// One reusable line buffer: a body is thousands of ~128-byte yEnc lines, and
	// the concat-then-convert form allocated twice per line.
	buf := make([]byte, 0, 512)
	for {
		line, err := c.conn.ReadLine()
		if err != nil {
			return written, err
		}
		if line == "." {
			break
		}
		buf = append(append(buf[:0], line...), '\r', '\n')
		n, err = w.Write(buf)
		written += int64(n)
		if err != nil {
			return written, err
		}
	}

	n, err = w.Write([]byte(".\r\n"))
	written += int64(n)
	if err != nil {
		return written, err
	}
	if c.pool != nil {
		c.pool.TrackRead(int(written))
	}
	return written, nil
}

func (c *Client) GetHead(messageID string) (string, error) {
	return c.readCommand("HEAD", 221, messageID)
}

func (c *Client) CheckArticle(messageID string) (bool, error) {
	c.setDeadline()
	id, err := c.conn.Cmd("STAT %s", formatMessageID(messageID))
	if err != nil {
		return false, err
	}

	c.conn.StartResponse(id)
	defer c.conn.EndResponse(id)

	code, _, err := c.conn.ReadCodeLine(223)
	if err != nil {
		if code == 430 {
			return false, nil
		}
		return false, err
	}

	return true, nil
}
