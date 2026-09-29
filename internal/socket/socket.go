package socket

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/dector/gust/internal/comments"
	"github.com/dector/gust/internal/config"
	"github.com/dector/gust/internal/coordinator"
	"github.com/dector/gust/internal/logger"
	"github.com/dector/gust/internal/protocol"
)

const socketDirMode = 0o700

type commentStore interface {
	NextBatch(context.Context) (comments.Batch, error)
	NextOne(context.Context) (comments.Batch, error)
	PeekBatch(context.Context) (comments.Batch, error)
	PeekOne(context.Context) (comments.Batch, error)
	ListUnfinished(context.Context) ([]comments.Comment, error)
	ListStates(context.Context, []comments.State) ([]comments.Comment, error)
	Watch(context.Context, int64) ([]comments.Comment, int64, error)
	ListSeenUnfinished(context.Context) ([]comments.Comment, error)
	MarkSeen(context.Context, string) (comments.Comment, error)
	MarkDone(context.Context, string) (comments.Comment, error)
	Reply(context.Context, string, comments.Author, string) (comments.Comment, error)
	Review(context.Context, string, string) (comments.Comment, error)
}

type control interface {
	Trigger(coordinator.TriggerSource, string) bool
	Status(context.Context) (coordinator.Status, error)
	SetAutoReload(context.Context, bool) (bool, error)
	Logs(context.Context) (coordinator.Logs, error)
}

// Server accepts local JSON control requests over a Unix socket.
type Server struct {
	path        string
	primaryPath string
	root        string
	ln          net.Listener
	file        os.FileInfo
	log         *logger.Logger

	urlMu        sync.RWMutex
	tailscaleURL string
	acceptOnce   sync.Once
	removeOnce   sync.Once
}

// Start creates and serves Gust's local control socket.
func Start(ctx context.Context, cfg config.Config, log *logger.Logger, ctl control, stores ...commentStore) (*Server, error) {
	primaryPath, err := Path(cfg.Root)
	if err != nil {
		return nil, err
	}
	root := cfg.Root
	if root == "" {
		root, err = os.Getwd()
		if err != nil {
			return nil, err
		}
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	lock, err := lockSocket(primaryPath)
	if err != nil {
		return nil, err
	}
	path, ln, file, err := listen(primaryPath)
	unlockSocket(lock)
	if err != nil {
		return nil, err
	}
	s := &Server{path: path, primaryPath: primaryPath, root: root, ln: ln, file: file, log: log}
	go func() {
		<-ctx.Done()
		s.Close()
	}()
	var store commentStore
	if len(stores) > 0 {
		store = stores[0]
	}
	go s.serve(ctx, ctl, store)
	return s, nil
}

// Path returns the deterministic socket path for root.
func Path(root string) (string, error) {
	if root == "" {
		var err error
		root, err = os.Getwd()
		if err != nil {
			return "", err
		}
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(abs))
	hash := hex.EncodeToString(sum[:])[:32]
	return filepath.Join(Dir(), hash+".sock"), nil
}

// Dir is the per-user directory containing Gust control sockets.
func Dir() string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("gust-%d", os.Getuid()))
}

// SetTailscaleURL publishes the URL reported by Tailscale Serve.
func (s *Server) SetTailscaleURL(url string) {
	s.urlMu.Lock()
	s.tailscaleURL = url
	s.urlMu.Unlock()
}

// Path returns the socket filesystem path.
func (s *Server) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// StopAccepting stops accepting new requests and releases this server's socket path.
func (s *Server) StopAccepting() error {
	if s == nil {
		return nil
	}
	var err error
	s.acceptOnce.Do(func() {
		if s.ln != nil {
			err = s.ln.Close()
		}
		// Release the path before another server can claim it. Otherwise a later
		// Remove may mistake the replacement for this socket if its inode is reused.
		if removeErr := s.Remove(); err == nil {
			err = removeErr
		}
	})
	return err
}

// Remove removes the socket file.
func (s *Server) Remove() error {
	if s == nil {
		return nil
	}
	var err error
	s.removeOnce.Do(func() {
		var lock *os.File
		lock, err = lockSocket(s.primaryPath)
		if err != nil {
			return
		}
		defer unlockSocket(lock)
		var current os.FileInfo
		current, err = os.Lstat(s.path)
		if errors.Is(err, os.ErrNotExist) {
			err = nil
			return
		}
		if err == nil && os.SameFile(s.file, current) {
			err = os.Remove(s.path)
		}
	})
	return err
}

// Close stops accepting requests and removes the socket file.
func (s *Server) Close() error {
	if s == nil {
		return nil
	}
	err := s.StopAccepting()
	if removeErr := s.Remove(); err == nil {
		err = removeErr
	}
	return err
}

func prepareSocketDir(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, socketDirMode); err != nil {
		return err
	}
	if err := os.Chmod(dir, socketDirMode); err != nil {
		return err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("socket parent is not a directory: %s", dir)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot inspect socket directory owner: %s", dir)
	}
	if int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("socket directory %s is not owned by current user", dir)
	}
	return nil
}

// The lock serializes path allocation and ownership-checked removal across processes.
func lockSocket(primaryPath string) (*os.File, error) {
	if err := prepareSocketDir(primaryPath); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(strings.TrimSuffix(primaryPath, ".sock")+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		lock.Close()
		return nil, err
	}
	return lock, nil
}

func unlockSocket(lock *os.File) {
	_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	_ = lock.Close()
}

func listen(primaryPath string) (string, net.Listener, os.FileInfo, error) {
	path := primaryPath
	if _, err := os.Lstat(path); err == nil {
		// A connectable socket belongs to a live instance. Never unlink it.
		conn, dialErr := net.DialTimeout("unix", path, 100*time.Millisecond)
		if dialErr == nil {
			conn.Close()
		} else if errors.Is(dialErr, syscall.ECONNREFUSED) || errors.Is(dialErr, syscall.ENOTSOCK) {
			if err := os.Remove(path); err != nil {
				return "", nil, nil, err
			}
		} else {
			// A timed-out or inaccessible socket might still be live.
			path = ""
		}
		if dialErr == nil {
			path = ""
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", nil, nil, err
	}
	for i := 0; i < 10; i++ {
		if path == "" {
			var suffix [8]byte
			if _, err := rand.Read(suffix[:]); err != nil {
				return "", nil, nil, err
			}
			path = strings.TrimSuffix(primaryPath, ".sock") + "-" + hex.EncodeToString(suffix[:]) + ".sock"
		}
		ln, err := net.Listen("unix", path)
		if errors.Is(err, syscall.EADDRINUSE) {
			path = ""
			continue
		}
		if err != nil {
			return "", nil, nil, err
		}
		ln.(*net.UnixListener).SetUnlinkOnClose(false)
		file, err := os.Lstat(path)
		if err != nil {
			ln.Close()
			return "", nil, nil, err
		}
		return path, ln, file, nil
	}
	return "", nil, nil, fmt.Errorf("cannot allocate control socket for %s", primaryPath)
}

func (s *Server) serve(ctx context.Context, ctl control, store commentStore) {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			if s.log != nil {
				s.log.Printf("socket accept failed: %v", err)
			}
			continue
		}
		go s.handle(ctx, conn, ctl, store)
	}
}

func (s *Server) handle(ctx context.Context, conn net.Conn, ctl control, store commentStore) {
	defer conn.Close()
	enc := json.NewEncoder(conn)
	if ctx.Err() != nil {
		_ = enc.Encode(protocol.Response{OK: false, Error: protocol.ErrShuttingDown})
		return
	}
	var req protocol.Request
	dec := json.NewDecoder(conn)
	if err := dec.Decode(&req); err != nil {
		_ = enc.Encode(protocol.Response{OK: false, Error: protocol.ErrInvalidRequest})
		return
	}
	if s.log != nil {
		s.log.Verbosef("socket request: %s", req.Action)
	}
	switch req.Action {
	case protocol.ActionCommentsWait, protocol.ActionCommentsWatch, protocol.ActionCommentsList, protocol.ActionCommentsPending, protocol.ActionCommentsSeen, protocol.ActionCommentsReply, protocol.ActionCommentsReview, protocol.ActionCommentsDone:
		if store == nil {
			_ = enc.Encode(protocol.Response{OK: false, Error: protocol.ErrCommentsDisabled})
			return
		}
		s.handleComments(ctx, conn, req, store)
	case protocol.ActionRerun:
		if !ctl.Trigger(coordinator.TriggerAgent, "socket") {
			_ = enc.Encode(protocol.Response{OK: false, Error: protocol.ErrShuttingDown})
			return
		}
		_ = enc.Encode(protocol.Response{OK: true, Status: "queued"})
	case protocol.ActionPause, protocol.ActionResume:
		paused := req.Action == protocol.ActionPause
		state, err := ctl.SetAutoReload(ctx, paused)
		if err != nil {
			_ = enc.Encode(protocol.Response{OK: false, Error: protocol.ErrShuttingDown})
			return
		}
		_ = enc.Encode(protocol.Response{OK: true, AutoReload: autoReloadValue(state)})
	case protocol.ActionStatus:
		status, err := ctl.Status(ctx)
		if err != nil {
			_ = enc.Encode(protocol.Response{OK: false, Error: protocol.ErrShuttingDown})
			return
		}
		s.urlMu.RLock()
		url := s.tailscaleURL
		s.urlMu.RUnlock()
		resp := protocol.Response{
			OK:           true,
			Root:         s.root,
			TailscaleURL: url,
			State:        string(status.State),
			PID:          status.PID,
			AppPort:      status.AppPort,
			ProxyPort:    status.ProxyPort,
			Version:      status.Version,
			AutoReload:   autoReloadValue(status.AutoReloadPaused),
		}
		if exit := status.Process.LastExit; exit != nil {
			resp.LastExit = &protocol.ExitSummary{
				Code:  exit.Code,
				At:    exit.At.UTC().Format(time.RFC3339Nano),
				Error: exit.Error,
			}
		}
		_ = enc.Encode(resp)
	case protocol.ActionLogs:
		logs, err := ctl.Logs(ctx)
		if errors.Is(err, coordinator.ErrNoLogs) {
			_ = enc.Encode(protocol.Response{OK: false, Error: protocol.ErrNoFailureLogs})
			return
		}
		if err != nil {
			_ = enc.Encode(protocol.Response{OK: false, Error: protocol.ErrShuttingDown})
			return
		}
		_ = enc.Encode(protocol.Response{
			OK:      true,
			Code:    logs.Code,
			At:      logs.At.UTC().Format(time.RFC3339Nano),
			Phase:   logs.Phase,
			Command: logs.Command,
			Stdout:  logs.Stdout,
			Stderr:  logs.Stderr,
		})
	default:
		_ = enc.Encode(protocol.Response{OK: false, Error: protocol.ErrInvalidRequest})
	}
}

func (s *Server) handleComments(ctx context.Context, conn net.Conn, req protocol.Request, store commentStore) {
	enc := json.NewEncoder(conn)
	var resp protocol.Response
	switch req.Action {
	case protocol.ActionCommentsWait:
		waitCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		// Detect a disconnected client while NextBatch is blocked. Closing the
		// connection on return also releases this reader goroutine.
		go func() {
			var b [1]byte
			_, _ = conn.Read(b[:])
			cancel()
		}()
		var batch comments.Batch
		var err error
		switch {
		case req.Peek && req.One:
			batch, err = store.PeekOne(waitCtx)
		case req.Peek:
			batch, err = store.PeekBatch(waitCtx)
		case req.One:
			batch, err = store.NextOne(waitCtx)
		default:
			batch, err = store.NextBatch(waitCtx)
		}
		if err != nil {
			if waitCtx.Err() != nil {
				return
			}
			resp = protocol.Response{OK: false, Error: protocol.ErrCommentStore}
		} else {
			resp = protocol.Response{OK: true, Batch: batch}
		}
	case protocol.ActionCommentsWatch:
		watchCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		// Detect a disconnected client while Watch is blocked.
		go func() {
			var b [1]byte
			_, _ = conn.Read(b[:])
			cancel()
		}()
		cs, cursor, err := store.Watch(watchCtx, req.Since)
		if err != nil {
			if watchCtx.Err() != nil {
				return
			}
			resp = protocol.Response{OK: false, Error: protocol.ErrCommentStore}
		} else {
			resp = protocol.Response{OK: true, Comments: cs, Cursor: cursor}
		}
	case protocol.ActionCommentsList:
		var cs []comments.Comment
		var err error
		if len(req.Filter) > 0 {
			states := make([]comments.State, len(req.Filter))
			for i, state := range req.Filter {
				states[i] = comments.State(state)
			}
			cs, err = store.ListStates(ctx, states)
		} else {
			cs, err = store.ListUnfinished(ctx)
		}
		if err != nil {
			resp = protocol.Response{OK: false, Error: protocol.ErrCommentStore}
		} else {
			resp = protocol.Response{OK: true, Comments: cs}
		}
	case protocol.ActionCommentsPending:
		cs, err := store.ListSeenUnfinished(ctx)
		if err != nil {
			resp = protocol.Response{OK: false, Error: protocol.ErrCommentStore}
		} else {
			resp = protocol.Response{OK: true, Comments: cs}
		}
	case protocol.ActionCommentsSeen:
		c, err := store.MarkSeen(ctx, req.ID)
		resp = commentMutationResponse(c, err)
	case protocol.ActionCommentsReply:
		author := comments.AuthorAgent
		if req.Human {
			author = comments.AuthorHuman
		}
		c, err := store.Reply(ctx, req.ID, author, req.Text)
		resp = commentMutationResponse(c, err)
	case protocol.ActionCommentsReview:
		c, err := store.Review(ctx, req.ID, req.Text)
		resp = commentMutationResponse(c, err)
	case protocol.ActionCommentsDone:
		c, err := store.MarkDone(ctx, req.ID)
		resp = commentMutationResponse(c, err)
	}
	_ = enc.Encode(resp)
}

func commentMutationResponse(c comments.Comment, err error) protocol.Response {
	switch {
	case err == nil:
		return protocol.Response{OK: true, Comment: c}
	case errors.Is(err, comments.ErrNotFound):
		return protocol.Response{OK: false, Error: protocol.ErrCommentNotFound}
	case errors.Is(err, comments.ErrInvalidState):
		return protocol.Response{OK: false, Error: protocol.ErrCommentInvalidState}
	case errors.Is(err, comments.ErrTextRequired):
		return protocol.Response{OK: false, Error: protocol.ErrCommentTextRequired}
	default:
		return protocol.Response{OK: false, Error: protocol.ErrCommentStore}
	}
}

func autoReloadValue(paused bool) string {
	if paused {
		return protocol.AutoReloadPaused
	}
	return protocol.AutoReloadActive
}
