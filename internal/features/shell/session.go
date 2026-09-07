package shell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/juex-ai/juex/internal/foundation/command"
	"github.com/juex-ai/juex/internal/foundation/sandbox"
	toolcore "github.com/juex-ai/juex/internal/foundation/tools"
)

const (
	defaultShellMaxSessions      = 64
	defaultShellMaxOutputTokens  = toolcore.DefaultCommandOutputBytes / 4
	defaultShellCompletedSession = 30 * time.Minute
	maxShellDeltaBytes           = 8 << 10
	maxShellDeltaCount           = 10_000
	minShellYield                = 250 * time.Millisecond
	defaultShellExecYield        = 10 * time.Second
	defaultShellInputWriteYield  = 250 * time.Millisecond
	defaultShellInputPollYield   = 5 * time.Second
	maxShellYield                = 30 * time.Second
	maxShellInputPollYield       = 5 * time.Minute
	defaultShellCloseWait        = 2 * time.Second
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
	Env             []string
	Cwd             string
	WorkDir         string
	FilePolicy      sandbox.FilePolicy
	Sandbox         sandbox.Policy
	SandboxRunner   sandbox.Runner
	Yield           time.Duration
	MaxOutputTokens int
	TTY             bool
	CallContext     context.Context
	Events          toolcore.ToolCallEvents
}

type ShellContinueRequest struct {
	SessionID       int
	Stdin           string
	Yield           time.Duration
	MaxOutputTokens int
	CallContext     context.Context
	Events          toolcore.ToolCallEvents
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
	BinaryOmitted      bool
	BinaryBytes        int
	BinarySHA256       string
	FirstBytesHex      string
}

type ShellSessionListResult struct {
	Sessions []ShellSessionInfo `json:"sessions"`
}

type ShellSessionInfo struct {
	SessionID    int       `json:"session_id"`
	Command      string    `json:"command"`
	Workdir      string    `json:"workdir"`
	Running      bool      `json:"running"`
	TTY          bool      `json:"tty"`
	StartedAt    time.Time `json:"started_at"`
	AgeMS        int64     `json:"age_ms"`
	LastAccessAt time.Time `json:"last_access_at"`
	IdleMS       int64     `json:"idle_ms"`
	ChunkID      int       `json:"chunk_id"`
	UnreadBytes  int       `json:"unread_bytes"`
	ExitCode     *int      `json:"exit_code"`
	TimedOut     bool      `json:"timed_out"`
}

func NewShellResult(result ShellSessionResult) toolcore.CommandResult {
	return toolcore.CommandResult{
		SessionID:          result.SessionID,
		Output:             result.Output,
		ExitCode:           cloneIntPtr(result.ExitCode),
		Running:            result.Running,
		TimedOut:           result.TimedOut,
		WallTimeMS:         result.WallTime.Milliseconds(),
		ChunkID:            result.ChunkID,
		OriginalBytes:      result.OriginalBytes,
		OriginalTokenCount: result.OriginalTokenCount,
		Truncated:          result.Truncated,
		BinaryOmitted:      result.BinaryOmitted,
		BinaryBytes:        result.BinaryBytes,
		BinarySHA256:       result.BinarySHA256,
		FirstBytesHex:      result.FirstBytesHex,
	}
}

type shellSession struct {
	id            int
	command       string
	workdir       string
	started       time.Time
	lastAccess    time.Time
	cmd           *exec.Cmd
	stdin         io.WriteCloser
	cancel        context.CancelFunc
	events        toolcore.ToolCallEvents
	maxTranscript int
	tty           bool
	waitFunc      func() error
	killFunc      func() error
	outputDone    <-chan struct{}
	doneChan      chan struct{}

	// deliveryMu keeps unread output snapshots from overtaking the matching
	// output-delta delivery after the bytes have been appended under mu.
	deliveryMu          sync.Mutex
	invocationMu        sync.Mutex
	mu                  sync.Mutex
	transcript          toolcore.CommandOutputBuffer
	unread              toolcore.CommandOutputBuffer
	deltaPending        []byte
	unownedDeltaPending []byte
	chunkID             int
	deltaCount          int
	done                bool
	timedOut            bool
	exitCode            *int
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
		maxTranscript: toolcore.DefaultCommandOutputBytes,
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
	callCtx := req.CallContext
	if callCtx == nil {
		callCtx = context.Background()
	}
	spec, err := prepareShellExecSpec(callCtx, req)
	if err != nil {
		return ShellSessionResult{}, err
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ShellSessionResult{}, fmt.Errorf("exec_command: session manager closed")
	}
	id := m.allocateSessionIDLocked()
	m.mu.Unlock()

	procCtx, cancel := context.WithCancel(m.baseCtx)
	cmd := exec.CommandContext(procCtx, spec.Binary, spec.Args...)
	if len(spec.Env) > 0 {
		cmd.Env = append([]string(nil), spec.Env...)
	}
	workdir := spec.Dir
	if spec.Dir != "" {
		cmd.Dir = spec.Dir
	} else if cwd, err := os.Getwd(); err == nil {
		workdir = cwd
	}
	now := time.Now()
	session := &shellSession{
		id:            id,
		command:       req.Command,
		workdir:       workdir,
		started:       now,
		lastAccess:    now,
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
		command.ConfigureContext(cmd)
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

func prepareShellExecSpec(ctx context.Context, req ShellStartRequest) (sandbox.ExecSpec, error) {
	argv := append([]string(nil), req.Args...)
	argv = append(argv, req.Command)
	spec := sandbox.ExecSpec{
		Binary: req.Binary,
		Args:   argv,
		Dir:    req.Cwd,
		Env:    append([]string(nil), req.Env...),
	}
	if !req.Sandbox.Enabled {
		return spec, nil
	}
	runner := req.SandboxRunner
	if runner == nil {
		runner = sandbox.DefaultRunner{}
	}
	prepared, err := runner.Prepare(ctx, sandbox.Request{
		Policy:     req.Sandbox,
		WorkDir:    req.WorkDir,
		FilePolicy: req.FilePolicy,
		Spec:       spec,
	})
	if err != nil {
		return sandbox.ExecSpec{}, err
	}
	if err := ctx.Err(); err != nil {
		return sandbox.ExecSpec{}, err
	}
	return prepared, nil
}

func (m *ShellSessionManager) List(includeCompleted bool) []ShellSessionInfo {
	now := time.Now()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.pruneLocked(now)
	sessions := make([]*shellSession, 0, len(m.sessions))
	for _, session := range m.sessions {
		sessions = append(sessions, session)
	}
	m.mu.Unlock()

	infos := make([]ShellSessionInfo, 0, len(sessions))
	for _, session := range sessions {
		info := session.info(now)
		if !includeCompleted && !info.Running {
			continue
		}
		infos = append(infos, info)
	}
	sort.Slice(infos, func(i, j int) bool {
		return infos[i].SessionID < infos[j].SessionID
	})
	return infos
}

func (m *ShellSessionManager) Continue(req ShellContinueRequest) (ShellSessionResult, error) {
	callCtx := req.CallContext
	if callCtx == nil {
		callCtx = context.Background()
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ShellSessionResult{}, fmt.Errorf("write_stdin: session manager closed")
	}
	session := m.sessions[req.SessionID]
	m.mu.Unlock()
	if session == nil {
		return ShellSessionResult{}, fmt.Errorf("write_stdin: unknown session_id %d", req.SessionID)
	}
	session.invocationMu.Lock()
	defer session.invocationMu.Unlock()
	session.setInvocationEvents(req.Events)
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
	deadline := time.Now().Add(defaultShellCloseWait)
	for _, session := range sessions {
		session.waitDone(time.Until(deadline))
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
	if s.outputDone != nil {
		<-s.outputDone
	}
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
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	s.mu.Lock()
	s.transcript.Append(data, s.maxTranscript)
	s.unread.Append(data, s.maxTranscript)
	emit := s.events.Emit
	remainingDeltas := maxShellDeltaCount - s.deltaCount
	var deltas []toolcore.OutputDelta
	if emit == nil {
		// Completed bytes produced without an active invocation are not emitted
		// later under another tool call. Keep only an incomplete UTF-8 suffix so
		// a rune that crosses into the next invocation is not misclassified.
		unowned := make([]byte, 0, len(s.unownedDeltaPending)+len(s.deltaPending)+len(data))
		unowned = append(unowned, s.unownedDeltaPending...)
		unowned = append(unowned, s.deltaPending...)
		unowned = append(unowned, data...)
		_, s.unownedDeltaPending = completeUTF8Output(unowned)
		s.deltaPending = nil
		s.chunkID += shellOutputChunkCount(data)
	} else if remainingDeltas > 0 {
		live := make([]byte, 0, len(s.unownedDeltaPending)+len(s.deltaPending)+len(data))
		live = append(live, s.unownedDeltaPending...)
		live = append(live, s.deltaPending...)
		live = append(live, data...)
		s.unownedDeltaPending = nil
		data = live
		data, s.deltaPending = completeUTF8Output(data)
		if len(data) == 0 {
			s.mu.Unlock()
			return
		}
		sanitized := toolcore.SanitizeCommandOutputBytes(data)
		if sanitized.Binary.Omitted {
			s.chunkID++
			s.deltaCount++
			deltas = append(deltas, toolcore.OutputDelta{
				Name:          eventToolName(s.events),
				ToolUseID:     s.events.ToolUseID,
				SessionID:     fmt.Sprint(s.id),
				ChunkID:       s.chunkID,
				Stream:        "combined",
				Text:          sanitized.Text,
				BinaryOmitted: true,
				BinaryBytes:   sanitized.Binary.Bytes,
				BinarySHA256:  sanitized.Binary.SHA256,
				FirstBytesHex: sanitized.Binary.FirstBytesHex,
			})
		} else {
			for len(data) > 0 && remainingDeltas > 0 {
				n := shellDeltaChunkSize(data, maxShellDeltaBytes)
				s.chunkID++
				s.deltaCount++
				remainingDeltas--
				deltas = append(deltas, toolcore.OutputDelta{
					Name:      eventToolName(s.events),
					ToolUseID: s.events.ToolUseID,
					SessionID: fmt.Sprint(s.id),
					ChunkID:   s.chunkID,
					Stream:    "combined",
					Text:      string(data[:n]),
				})
				data = data[n:]
			}
		}
	} else {
		s.unownedDeltaPending = nil
		s.deltaPending = nil
	}
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
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastAccess = time.Now()
	outputLimit := s.maxTranscript
	if outputLimit <= 0 {
		outputLimit = toolcore.DefaultCommandOutputBytes
	}
	if maxOutputTokens > 0 && maxOutputTokens <= int(^uint(0)>>1)/4 {
		if requested := maxOutputTokens * 4; requested > 0 && requested < outputLimit {
			outputLimit = requested
		}
	}
	final := s.done
	snapshot := s.unread.Snapshot(outputLimit, final)
	carry := s.unread.PendingUTF8()
	if clearUnread {
		s.unread.Reset()
		if !final && len(carry) > 0 {
			s.unread.Append(carry, s.maxTranscript)
		}
	}
	if snapshot.TotalBytes == 0 && !clearUnread {
		snapshot = s.transcript.Snapshot(outputLimit, final)
	}
	s.events = toolcore.ToolCallEvents{}
	if final {
		s.deltaPending = nil
		s.unownedDeltaPending = nil
	}
	text := string(snapshot.Bytes)
	sessionID := 0
	if !s.done {
		sessionID = s.id
	}
	originalTokenCount := approxTokenCount(snapshot.TotalBytes)
	if snapshot.Binary.Omitted {
		originalTokenCount = approxTokenCountBytes([]byte(text))
	}
	return ShellSessionResult{
		SessionID:          sessionID,
		Output:             text,
		ExitCode:           cloneIntPtr(s.exitCode),
		Running:            !s.done,
		TimedOut:           s.timedOut,
		WallTime:           time.Since(s.started),
		ChunkID:            s.chunkID,
		OriginalBytes:      saturatingInt(snapshot.TotalBytes),
		OriginalTokenCount: originalTokenCount,
		Truncated:          snapshot.Truncated,
		BinaryOmitted:      snapshot.Binary.Omitted,
		BinaryBytes:        snapshot.Binary.Bytes,
		BinarySHA256:       snapshot.Binary.SHA256,
		FirstBytesHex:      snapshot.Binary.FirstBytesHex,
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
			return command.InterruptProcessGroup(s.cmd)
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

func (s *shellSession) waitDone(timeout time.Duration) bool {
	if s == nil || s.doneChan == nil {
		return true
	}
	if timeout <= 0 {
		select {
		case <-s.doneChan:
			return true
		default:
			return false
		}
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-s.doneChan:
		return true
	case <-timer.C:
		return false
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

func (s *shellSession) info(now time.Time) ShellSessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return ShellSessionInfo{
		SessionID:    s.id,
		Command:      s.command,
		Workdir:      s.workdir,
		Running:      !s.done,
		TTY:          s.tty,
		StartedAt:    s.started,
		AgeMS:        durationMillis(now.Sub(s.started)),
		LastAccessAt: s.lastAccess,
		IdleMS:       durationMillis(now.Sub(s.lastAccess)),
		ChunkID:      s.chunkID,
		UnreadBytes:  saturatingInt(s.unread.TotalBytes()),
		ExitCode:     cloneIntPtr(s.exitCode),
		TimedOut:     s.timedOut,
	}
}

func (s *shellSession) setInvocationEvents(toolEvents toolcore.ToolCallEvents) {
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	s.mu.Lock()
	s.events = toolEvents
	s.mu.Unlock()
}

func shellDeltaChunkSize(data []byte, limit int) int {
	if len(data) <= limit {
		return len(data)
	}
	n := toolcore.ValidUTF8PrefixLength(data, limit)
	if n <= 0 || n > limit {
		return limit
	}
	return n
}

func shellOutputChunkCount(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	if toolcore.SanitizeCommandOutputBytes(data).Binary.Omitted {
		return 1
	}
	count := 0
	for len(data) > 0 {
		n := shellDeltaChunkSize(data, maxShellDeltaBytes)
		data = data[n:]
		count++
	}
	return count
}

func completeUTF8Output(data []byte) ([]byte, []byte) {
	for index := 0; index < len(data); {
		if !utf8.FullRune(data[index:]) {
			return data[:index], append([]byte(nil), data[index:]...)
		}
		r, size := utf8.DecodeRune(data[index:])
		if r == utf8.RuneError && size == 1 {
			return data, nil
		}
		index += size
	}
	return data, nil
}

func approxTokenCountBytes(data []byte) int {
	return approxTokenCount(int64(len(data)))
}

func approxTokenCount(byteCount int64) int {
	if byteCount == 0 {
		return 0
	}
	return saturatingInt((byteCount + 3) / 4)
}

func saturatingInt(value int64) int {
	if value <= 0 {
		return 0
	}
	maxInt := int64(^uint(0) >> 1)
	if value > maxInt {
		return int(maxInt)
	}
	return int(value)
}

func durationMillis(d time.Duration) int64 {
	if d < 0 {
		return 0
	}
	return d.Milliseconds()
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

func eventToolName(events toolcore.ToolCallEvents) string {
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
