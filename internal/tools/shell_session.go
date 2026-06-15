package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultShellMaxSessions      = 64
	defaultShellMaxOutputTokens  = 10000
	defaultShellTranscriptBytes  = 1 << 20
	defaultShellCompletedSession = 30 * time.Minute
	maxShellDeltaBytes           = 8 << 10
	minShellYield                = 250 * time.Millisecond
	defaultShellExecYield        = 10 * time.Second
	defaultShellInputWriteYield  = 250 * time.Millisecond
	defaultShellInputPollYield   = 5 * time.Second
	maxShellYield                = 30 * time.Second
	maxShellInputPollYield       = 5 * time.Minute
	shellExitSettleGrace         = 100 * time.Millisecond
	shellInterruptInput          = "\x03"
)

type ShellSessionManager struct {
	baseCtx       context.Context
	maxSessions   int
	maxTranscript int
	completedTTL  time.Duration
	mu            sync.Mutex
	nextSessionID int
	sessions      map[int]*shellSession
	closed        bool
}

type ShellStartRequest struct {
	Binary          string
	Args            []string
	Command         string
	Cwd             string
	Timeout         time.Duration
	Yield           time.Duration
	MaxOutputTokens int
	TTY             bool
	CallContext     context.Context
	Events          ToolCallEvents
}

type ShellContinueRequest struct {
	SessionID       int
	Stdin           string
	Yield           time.Duration
	MaxOutputTokens int
	CallContext     context.Context
}

type ShellSessionResult struct {
	SessionID          int
	Output             string
	ExitCode           *int
	Running            bool
	TimedOut           bool
	WallTime           time.Duration
	ChunkID            int
	OriginalBytes      int
	OriginalTokenCount int
	Truncated          bool
}

type shellSession struct {
	id            int
	started       time.Time
	lastAccess    time.Time
	cmd           *exec.Cmd
	stdin         io.WriteCloser
	cancel        context.CancelFunc
	events        ToolCallEvents
	maxTranscript int
	tty           bool
	waitFunc      func() error
	killFunc      func() error
	doneChan      chan struct{}

	mu              sync.Mutex
	transcript      []byte
	unread          []byte
	unreadTruncated bool
	chunkID         int
	done            bool
	timedOut        bool
	exitCode        *int
}

type shellSessionWriter struct {
	session *shellSession
}

func NewShellSessionManager(ctx context.Context) *ShellSessionManager {
	if ctx == nil {
		ctx = context.Background()
	}
	return &ShellSessionManager{
		baseCtx:       ctx,
		maxSessions:   defaultShellMaxSessions,
		maxTranscript: defaultShellTranscriptBytes,
		completedTTL:  defaultShellCompletedSession,
		nextSessionID: 1,
		sessions:      make(map[int]*shellSession),
	}
}

func (m *ShellSessionManager) Start(req ShellStartRequest) (ShellSessionResult, error) {
	if strings.TrimSpace(req.Binary) == "" {
		return ShellSessionResult{}, fmt.Errorf("exec_command: missing shell binary")
	}
	if req.Command == "" {
		return ShellSessionResult{}, fmt.Errorf("exec_command: missing cmd")
	}
	if req.Timeout <= 0 {
		req.Timeout = time.Duration(DefaultTimeoutSeconds) * time.Second
	}
	callCtx := req.CallContext
	if callCtx == nil {
		callCtx = context.Background()
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ShellSessionResult{}, fmt.Errorf("exec_command: session manager closed")
	}
	id := m.allocateSessionIDLocked()
	m.mu.Unlock()

	procCtx, cancel := context.WithTimeout(m.baseCtx, req.Timeout)
	argv := append([]string(nil), req.Args...)
	argv = append(argv, req.Command)
	cmd := exec.CommandContext(procCtx, req.Binary, argv...)
	if req.Cwd != "" {
		cmd.Dir = req.Cwd
	}
	session := &shellSession{
		id:            id,
		started:       time.Now(),
		lastAccess:    time.Now(),
		cmd:           cmd,
		cancel:        cancel,
		events:        req.Events,
		maxTranscript: m.maxTranscript,
		tty:           req.TTY,
		doneChan:      make(chan struct{}),
	}
	if req.TTY {
		stdin, err := startPTYSession(cmd, session)
		if err != nil {
			cancel()
			return ShellSessionResult{}, err
		}
		session.stdin = stdin
	} else {
		cmd.Stdout = shellSessionWriter{session: session}
		cmd.Stderr = shellSessionWriter{session: session}
		configureCommandForContext(cmd)
		if err := cmd.Start(); err != nil {
			cancel()
			return ShellSessionResult{}, err
		}
	}
	go session.wait(procCtx)

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		session.kill()
		return ShellSessionResult{}, fmt.Errorf("exec_command: session manager closed")
	}
	m.pruneLocked(time.Now())
	if len(m.sessions) >= m.maxSessions {
		m.mu.Unlock()
		session.kill()
		return ShellSessionResult{}, fmt.Errorf("exec_command: too many active sessions (%d)", m.maxSessions)
	}
	m.sessions[id] = session
	m.mu.Unlock()

	session.waitFor(callCtx, defaultDuration(req.Yield, defaultShellExecYield), minShellYield, maxShellYield)
	return session.snapshot(true, req.MaxOutputTokens), nil
}

func (m *ShellSessionManager) Continue(req ShellContinueRequest) (ShellSessionResult, error) {
	callCtx := req.CallContext
	if callCtx == nil {
		callCtx = context.Background()
	}
	m.mu.Lock()
	session := m.sessions[req.SessionID]
	m.mu.Unlock()
	if session == nil {
		return ShellSessionResult{}, fmt.Errorf("write_stdin: unknown session_id %d", req.SessionID)
	}
	if req.Stdin != "" {
		if err := session.writeStdin(req.Stdin); err != nil {
			return session.snapshot(false, req.MaxOutputTokens), err
		}
	}
	yield := req.Yield
	minYield := minShellYield
	maxYield := maxShellYield
	if req.Stdin == "" {
		yield = defaultDuration(yield, defaultShellInputPollYield)
		minYield = defaultShellInputPollYield
		maxYield = maxShellInputPollYield
	} else {
		yield = defaultDuration(yield, defaultShellInputWriteYield)
	}
	session.waitFor(callCtx, yield, minYield, maxYield)
	return session.snapshot(true, req.MaxOutputTokens), nil
}

func (m *ShellSessionManager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	sessions := make([]*shellSession, 0, len(m.sessions))
	for _, session := range m.sessions {
		sessions = append(sessions, session)
	}
	m.sessions = make(map[int]*shellSession)
	m.mu.Unlock()
	for _, session := range sessions {
		session.kill()
	}
	return nil
}

func (m *ShellSessionManager) pruneLocked(now time.Time) {
	for id, session := range m.sessions {
		if session.isDone() && now.Sub(session.lastAccessTime()) > m.completedTTL {
			delete(m.sessions, id)
		}
	}
	if len(m.sessions) < m.maxSessions {
		return
	}
	completed := make([]*shellSession, 0, len(m.sessions))
	for _, session := range m.sessions {
		if session.isDone() {
			completed = append(completed, session)
		}
	}
	sort.Slice(completed, func(i, j int) bool {
		return completed[i].lastAccessTime().Before(completed[j].lastAccessTime())
	})
	for _, session := range completed {
		if len(m.sessions) < m.maxSessions {
			return
		}
		delete(m.sessions, session.id)
	}
}

func (s *shellSession) wait(ctx context.Context) {
	if s.cancel != nil {
		defer s.cancel()
	}
	err := s.waitProcess()
	s.mu.Lock()
	s.done = true
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		s.timedOut = true
	}
	if err != nil {
		var codeErr *shellExitCodeError
		var exitErr *exec.ExitError
		if errors.As(err, &codeErr) {
			code := codeErr.code
			s.exitCode = &code
		} else if errors.As(err, &exitErr) {
			code := exitErr.ExitCode()
			s.exitCode = &code
		} else if ctx.Err() != nil {
			code := -1
			s.exitCode = &code
		} else {
			code := -1
			s.exitCode = &code
		}
	} else {
		code := 0
		s.exitCode = &code
	}
	s.mu.Unlock()
	close(s.doneChan)
}

func (s *shellSession) waitProcess() error {
	if s.waitFunc != nil {
		return s.waitFunc()
	}
	return s.cmd.Wait()
}

func (s *shellSession) Write(p []byte) (int, error) {
	s.appendOutput(p)
	return len(p), nil
}

func (w shellSessionWriter) Write(p []byte) (int, error) {
	if w.session == nil {
		return len(p), nil
	}
	return w.session.Write(p)
}

func (s *shellSession) appendOutput(p []byte) {
	if len(p) == 0 {
		return
	}
	data := append([]byte(nil), p...)
	deltas := make([]OutputDelta, 0, (len(data)/maxShellDeltaBytes)+1)
	s.mu.Lock()
	s.transcript = appendCappedBytes(s.transcript, data, s.maxTranscript)
	beforeUnread := len(s.unread)
	s.unread = appendCappedBytes(s.unread, data, s.maxTranscript)
	if beforeUnread+len(data) > s.maxTranscript {
		s.unreadTruncated = true
	}
	for len(data) > 0 {
		n := len(data)
		if n > maxShellDeltaBytes {
			n = maxShellDeltaBytes
		}
		s.chunkID++
		deltas = append(deltas, OutputDelta{
			Name:      eventToolName(s.events),
			ToolUseID: s.events.ToolUseID,
			SessionID: fmt.Sprint(s.id),
			ChunkID:   s.chunkID,
			Stream:    "combined",
			Text:      string(data[:n]),
			Truncated: false,
		})
		data = data[n:]
	}
	emit := s.events.Emit
	s.mu.Unlock()
	if emit == nil {
		return
	}
	for _, delta := range deltas {
		emit(delta)
	}
}

func (s *shellSession) waitFor(ctx context.Context, yield time.Duration, minYield time.Duration, maxYield time.Duration) {
	timer := time.NewTimer(clampShellYield(yield, minYield, maxYield))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		s.kill()
	case <-timer.C:
		settle := time.NewTimer(shellExitSettleGrace)
		defer settle.Stop()
		select {
		case <-ctx.Done():
			s.kill()
		case <-s.doneChan:
		case <-settle.C:
		}
	case <-s.doneChan:
	}
}

func (s *shellSession) snapshot(clearUnread bool, maxOutputTokens int) ShellSessionResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastAccess = time.Now()
	output := append([]byte(nil), s.unread...)
	truncated := s.unreadTruncated
	if clearUnread {
		s.unread = nil
		s.unreadTruncated = false
	}
	if len(output) == 0 && !clearUnread {
		output = append([]byte(nil), s.transcript...)
	}
	text, capped := capOutputText(string(output), maxOutputTokens)
	if capped {
		truncated = true
	}
	sessionID := 0
	if !s.done {
		sessionID = s.id
	}
	return ShellSessionResult{
		SessionID:          sessionID,
		Output:             text,
		ExitCode:           cloneIntPtr(s.exitCode),
		Running:            !s.done,
		TimedOut:           s.timedOut,
		WallTime:           time.Since(s.started),
		ChunkID:            s.chunkID,
		OriginalBytes:      len(output),
		OriginalTokenCount: approxTokenCount(string(output)),
		Truncated:          truncated,
	}
}

func (s *shellSession) writeStdin(input string) error {
	s.mu.Lock()
	done := s.done
	stdin := s.stdin
	tty := s.tty
	s.mu.Unlock()
	if done {
		return fmt.Errorf("write_stdin: session %d has already exited", s.id)
	}
	if !tty {
		if input == shellInterruptInput {
			return interruptCommandProcessGroup(s.cmd)
		}
		return fmt.Errorf("write_stdin: stdin is closed for this session; rerun exec_command with tty=true to keep stdin open")
	}
	if stdin == nil {
		return fmt.Errorf("write_stdin: session %d has no stdin", s.id)
	}
	_, err := io.WriteString(stdin, input)
	return err
}

func (s *shellSession) kill() {
	if s.cancel != nil {
		s.cancel()
	}
	if s.killFunc != nil {
		_ = s.killFunc()
	}
	if s.stdin != nil {
		_ = s.stdin.Close()
	}
}

func (s *shellSession) isDone() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.done
}

func (s *shellSession) lastAccessTime() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastAccess
}

func appendCappedBytes(dst, src []byte, limit int) []byte {
	if limit <= 0 {
		return append(dst, src...)
	}
	dst = append(dst, src...)
	if len(dst) <= limit {
		return dst
	}
	return append([]byte(nil), dst[len(dst)-limit:]...)
}

func capOutputText(text string, maxOutputTokens int) (string, bool) {
	if maxOutputTokens <= 0 {
		return text, false
	}
	limit := maxOutputTokens * 4
	if limit < 1 || len(text) <= limit {
		return text, false
	}
	omitted := len(text) - limit
	return fmt.Sprintf("[output truncated: %d earlier bytes omitted]\n%s", omitted, text[len(text)-limit:]), true
}

func approxTokenCount(text string) int {
	if text == "" {
		return 0
	}
	return (len(text) + 3) / 4
}

func clampShellYield(d time.Duration, minYield time.Duration, maxYield time.Duration) time.Duration {
	if minYield <= 0 {
		minYield = minShellYield
	}
	if maxYield <= 0 {
		maxYield = maxShellYield
	}
	if d < minYield {
		return minYield
	}
	if d > maxYield {
		return maxYield
	}
	return d
}

func defaultDuration(value time.Duration, fallback time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return fallback
}

func (m *ShellSessionManager) allocateSessionIDLocked() int {
	if m.nextSessionID <= 0 {
		m.nextSessionID = 1
	}
	id := m.nextSessionID
	m.nextSessionID++
	return id
}

func cloneIntPtr(v *int) *int {
	if v == nil {
		return nil
	}
	copied := *v
	return &copied
}

func eventToolName(events ToolCallEvents) string {
	if events.Name != "" {
		return events.Name
	}
	return "exec_command"
}

type shellExitCodeError struct {
	code int
}

func (e *shellExitCodeError) Error() string {
	return fmt.Sprintf("exit status %d", e.code)
}

var _ io.Writer = shellSessionWriter{}
