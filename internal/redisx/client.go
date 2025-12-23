package redisx

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const defaultTimeout = 5 * time.Second

// Config describes minimal connection parameters.
type Config struct {
	Addr     string
	Password string
	TLS      bool
}

// Client implements a lightweight Redis RESP client.
type Client struct {
	addr     string
	password string
	conn     net.Conn
	reader   *bufio.Reader
	timeout  time.Duration

	// RDB read timeout (configurable, default 60 seconds)
	rdbTimeout time.Duration

	mu     sync.Mutex
	closed bool
}

// Dial creates a new client connection.
func Dial(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.TLS {
		return nil, errors.New("redisx: TLS is not supported in this prototype")
	}
	if cfg.Addr == "" {
		return nil, errors.New("redisx: addr is empty")
	}
	dialer := &net.Dialer{}
	conn, err := dialer.DialContext(ctx, "tcp", cfg.Addr)
	if err != nil {
		return nil, fmt.Errorf("redisx: dial %s failed: %w", cfg.Addr, err)
	}

	// Enable TCP keepalive to align with Dragonfly's 30-second timeout detection
	// and increase receive buffer to prevent Dragonfly's sink->Write() from blocking
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		// Enable keepalive
		if err := tcpConn.SetKeepAlive(true); err != nil {
			// Non-fatal; log a warning and continue
			fmt.Fprintf(os.Stderr, "Warning: failed to enable TCP KeepAlive: %v\n", err)
		} else if err := tcpConn.SetKeepAlivePeriod(30 * time.Second); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to set KeepAlive period: %v\n", err)
		}

		// CRITICAL: Increase receive buffer to 16MB
		// Dragonfly's Channel buffer is only 2 elements, and sink->Write() blocks if
		// client doesn't read fast enough. With a large SO_RCVBUF, kernel can buffer
		// more data, allowing Dragonfly to continue sending even if application is busy
		// processing/writing to Redis.
		//
		// This prevents the 30-second timeout error:
		//   "Master detected replication timeout, breaking full sync"
		const recvBufferSize = 16 * 1024 * 1024 // 16MB
		if rawConn, err := tcpConn.SyscallConn(); err == nil {
			_ = rawConn.Control(func(fd uintptr) {
				// Set SO_RCVBUF on the socket file descriptor
				// syscall.SetsockoptInt requires proper imports
				if err := setReceiveBuffer(int(fd), recvBufferSize); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to set SO_RCVBUF to %d: %v\n", recvBufferSize, err)
				}
			})
		}
	}

	client := &Client{
		addr:       cfg.Addr,
		password:   cfg.Password,
		conn:       conn,
		reader:     bufio.NewReader(conn),
		timeout:    defaultTimeout,
		rdbTimeout: 60 * time.Second, // fixed 60s for snapshot/journal reads
	}

	if cfg.Password != "" {
		if _, err := client.Do("AUTH", cfg.Password); err != nil {
			client.Close()
			return nil, fmt.Errorf("redisx: auth failed: %w", err)
		}
	}
	if err := client.Ping(); err != nil {
		client.Close()
		return nil, err
	}
	return client, nil
}

// Close terminates the connection.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	return c.conn.Close()
}

// Ping verifies connectivity.
func (c *Client) Ping() error {
	_, err := c.Do("PING")
	return err
}

// RawRead reads directly from the underlying connection (RDB snapshot/journal)
// honoring the configured timeout (default 60s).
func (c *Client) RawRead(buf []byte) (int, error) {
	c.mu.Lock()
	timeout := c.rdbTimeout
	c.mu.Unlock()

	if c.closed {
		return 0, errors.New("redisx: client closed")
	}
	// Apply read deadline
	if err := c.conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return 0, err
	}
	return c.conn.Read(buf)
}

// Read implements io.Reader for journal processing.
// It reads via bufio.Reader to avoid skipping buffered bytes.
func (c *Client) Read(buf []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return 0, errors.New("redisx: client closed")
	}
	// Journal streams are long-lived; relax the read deadline (~24h)
	// and rely on TCP keepalive (30s) for liveness.
	if err := c.conn.SetReadDeadline(time.Now().Add(24 * time.Hour)); err != nil {
		return 0, err
	}
	// bufio.Reader manages buffering vs. the underlying conn
	return c.reader.Read(buf)
}

// Do sends a command and returns the parsed RESP reply.
func (c *Client) Do(cmd string, args ...interface{}) (interface{}, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, errors.New("redisx: client closed")
	}

	if err := c.writeCommand(cmd, args...); err != nil {
		return nil, err
	}
	return c.readReply()
}

// DoWithTimeout sends a command with a custom timeout and returns the parsed RESP reply.
// Useful for commands that may take longer than the default timeout (e.g., DFLY STARTSTABLE).
func (c *Client) DoWithTimeout(timeout time.Duration, cmd string, args ...interface{}) (interface{}, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, errors.New("redisx: client closed")
	}

	// Temporarily override timeout for write
	if err := c.conn.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	count := 1 + len(args)
	fmt.Fprintf(&buf, "*%d\r\n", count)
	writeBulk(&buf, strings.ToUpper(cmd))
	for _, arg := range args {
		writeBulk(&buf, formatArg(arg))
	}
	if _, err := c.conn.Write(buf.Bytes()); err != nil {
		return nil, err
	}

	// Set read deadline with custom timeout
	if err := c.conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return nil, err
	}
	return c.readReply()
}

// Set sets a key to the given value.
func (c *Client) Set(key, value string) error {
	_, err := c.Do("SET", key, value)
	return err
}

// SetPX sets key with PX ttl in milliseconds.
func (c *Client) SetPX(key string, value interface{}, ttl time.Duration) error {
	ms := ttl.Milliseconds()
	_, err := c.Do("SET", key, value, "PX", ms)
	return err
}

// GetString returns string value or empty string if nil.
func (c *Client) GetString(key string) (string, error) {
	reply, err := c.Do("GET", key)
	if err != nil {
		return "", err
	}
	if reply == nil {
		return "", nil
	}
	return ToString(reply)
}

// ScriptLoad loads Lua into Redis and returns SHA1.
func (c *Client) ScriptLoad(script string) (string, error) {
	reply, err := c.Do("SCRIPT", "LOAD", script)
	if err != nil {
		return "", err
	}
	return ToString(reply)
}

// ScriptLoadFile loads Lua script from file.
func (c *Client) ScriptLoadFile(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return c.ScriptLoad(string(content))
}

// EvalSha executes Lua script by SHA.
func (c *Client) EvalSha(sha string, keys []string, args []interface{}) (interface{}, error) {
	params := make([]interface{}, 0, 2+len(keys)+len(args))
	params = append(params, sha, len(keys))
	for _, k := range keys {
		params = append(params, k)
	}
	params = append(params, args...)
	return c.Do("EVALSHA", params...)
}

// Info returns server info section.
func (c *Client) Info(section string) (string, error) {
	reply, err := c.Do("INFO", section)
	if err != nil {
		return "", err
	}
	return ToString(reply)
}

func (c *Client) writeCommand(cmd string, args ...interface{}) error {
	if err := c.conn.SetWriteDeadline(time.Now().Add(c.timeout)); err != nil {
		return err
	}
	var buf bytes.Buffer
	count := 1 + len(args)
	fmt.Fprintf(&buf, "*%d\r\n", count)
	writeBulk(&buf, strings.ToUpper(cmd))
	for _, arg := range args {
		writeBulk(&buf, formatArg(arg))
	}
	if _, err := c.conn.Write(buf.Bytes()); err != nil {
		return err
	}
	if err := c.conn.SetReadDeadline(time.Now().Add(c.timeout)); err != nil {
		return err
	}
	return nil
}

func (c *Client) readReply() (interface{}, error) {
	line, err := c.reader.ReadByte()
	if err != nil {
		return nil, err
	}
	switch line {
	case '+':
		str, err := readLine(c.reader)
		return str, err
	case '-':
		msg, err := readLine(c.reader)
		if err != nil {
			return nil, err
		}
		return nil, errors.New("redis: " + msg)
	case ':':
		numStr, err := readLine(c.reader)
		if err != nil {
			return nil, err
		}
		val, err := strconv.ParseInt(numStr, 10, 64)
		if err != nil {
			return nil, err
		}
		return val, nil
	case '$':
		sizeStr, err := readLine(c.reader)
		if err != nil {
			return nil, err
		}
		size, err := strconv.Atoi(sizeStr)
		if err != nil {
			return nil, err
		}
		if size == -1 {
			return nil, nil
		}
		data := make([]byte, size+2)
		if _, err := io.ReadFull(c.reader, data); err != nil {
			return nil, err
		}
		return string(data[:size]), nil
	case '*':
		countStr, err := readLine(c.reader)
		if err != nil {
			return nil, err
		}
		count, err := strconv.Atoi(countStr)
		if err != nil {
			return nil, err
		}
		if count == -1 {
			return nil, nil
		}
		result := make([]interface{}, 0, count)
		for i := 0; i < count; i++ {
			item, err := c.readReply()
			if err != nil {
				return nil, err
			}
			result = append(result, item)
		}
		return result, nil
	default:
		return nil, fmt.Errorf("redisx: unexpected RESP prefix %q", line)
	}
}

func writeBulk(buf *bytes.Buffer, value string) {
	fmt.Fprintf(buf, "$%d\r\n%s\r\n", len(value), value)
}

func formatArg(arg interface{}) string {
	switch v := arg.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case uint64:
		return strconv.FormatUint(v, 10)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(v), 'f', -1, 32)
	case bool:
		if v {
			return "1"
		}
		return "0"
	case fmt.Stringer:
		return v.String()
	default:
		return fmt.Sprint(arg)
	}
}

func readLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	line = strings.TrimSuffix(line, "\n")
	line = strings.TrimSuffix(line, "\r")
	return line, nil
}

// ToString converts RESP reply to string.
func ToString(reply interface{}) (string, error) {
	switch v := reply.(type) {
	case string:
		return v, nil
	case []byte:
		return string(v), nil
	case int:
		return strconv.Itoa(v), nil
	case int64:
		return strconv.FormatInt(v, 10), nil
	case nil:
		return "", nil
	default:
		return "", fmt.Errorf("redisx: cannot convert %T to string", reply)
	}
}

// ToStringSlice converts RESP array to string slice.
func ToStringSlice(reply interface{}) ([]string, error) {
	arr, ok := reply.([]interface{})
	if !ok {
		return nil, fmt.Errorf("redisx: reply is not array (%T)", reply)
	}
	result := make([]string, 0, len(arr))
	for _, item := range arr {
		str, err := ToString(item)
		if err != nil {
			return nil, err
		}
		result = append(result, str)
	}
	return result, nil
}

// ToInt64 converts reply to int64.
func ToInt64(reply interface{}) (int64, error) {
	switch v := reply.(type) {
	case int64:
		return v, nil
	case int:
		return int64(v), nil
	case string:
		return strconv.ParseInt(v, 10, 64)
	case nil:
		return 0, errors.New("redisx: nil reply for integer conversion")
	default:
		return 0, fmt.Errorf("redisx: cannot convert %T to int64", reply)
	}
}

// IsMovedError reports whether the error is a MOVED redirection response from Redis Cluster.
func IsMovedError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToUpper(err.Error()), "MOVED")
}

// ParseMovedAddr extracts host:port from a MOVED error.
func ParseMovedAddr(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	parts := strings.Fields(err.Error())
	for i, token := range parts {
		if strings.EqualFold(token, "MOVED") && i+2 < len(parts) {
			addr := parts[i+2]
			// trim comma or other trailing characters
			addr = strings.Trim(addr, ",")
			return addr, true
		}
	}
	return "", false
}
