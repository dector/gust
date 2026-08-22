package socket

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/dector/gust/internal/config"
	"github.com/dector/gust/internal/coordinator"
	"github.com/dector/gust/internal/logger"
)

const socketDirMode = 0o700

type control interface {
	Trigger(coordinator.TriggerSource, string) bool
	Status(context.Context) (coordinator.Status, error)
}

// Server accepts local JSON control requests over a Unix socket.
type Server struct {
	path string
	ln   net.Listener
	log  *logger.Logger

	once sync.Once
}

// Start creates and serves Gust's local control socket.
func Start(ctx context.Context, cfg config.Config, log *logger.Logger, ctl control) (*Server, error) {
	path, err := Path(cfg.Root)
	if err != nil {
		return nil, err
	}
	if err := prepareSocket(path); err != nil {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	s := &Server{path: path, ln: ln, log: log}
	if log != nil {
		log.Printf("socket: %s", path)
	}
	go func() {
		<-ctx.Done()
		s.Close()
	}()
	go s.serve(ctx, ctl)
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
	return filepath.Join(os.TempDir(), fmt.Sprintf("gust-%d", os.Getuid()), hash+".sock"), nil
}

// Close stops accepting requests and removes the socket file.
func (s *Server) Close() error {
	if s == nil {
		return nil
	}
	var err error
	s.once.Do(func() {
		if s.ln != nil {
			err = s.ln.Close()
		}
		if removeErr := os.Remove(s.path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) && err == nil {
			err = removeErr
		}
	})
	return err
}

func prepareSocket(path string) error {
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
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (s *Server) serve(ctx context.Context, ctl control) {
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
		go s.handle(ctx, conn, ctl)
	}
}

type request struct {
	Action string `json:"action"`
}

type response struct {
	OK        bool                      `json:"ok"`
	Error     string                    `json:"error,omitempty"`
	Status    string                    `json:"status,omitempty"`
	State     coordinator.ExternalState `json:"state,omitempty"`
	PID       int                       `json:"pid,omitempty"`
	AppPort   int                       `json:"app_port,omitempty"`
	ProxyPort int                       `json:"proxy_port,omitempty"`
	Version   int                       `json:"version,omitempty"`
}

func (s *Server) handle(ctx context.Context, conn net.Conn, ctl control) {
	defer conn.Close()
	if ctx.Err() != nil {
		_ = json.NewEncoder(conn).Encode(response{OK: false, Error: "shutting_down"})
		return
	}
	var req request
	dec := json.NewDecoder(conn)
	if err := dec.Decode(&req); err != nil {
		_ = json.NewEncoder(conn).Encode(response{OK: false, Error: "invalid_request"})
		return
	}
	if s.log != nil {
		s.log.Verbosef("socket request: %s", req.Action)
	}
	switch req.Action {
	case "rerun":
		if !ctl.Trigger(coordinator.TriggerAgent, "socket") {
			_ = json.NewEncoder(conn).Encode(response{OK: false, Error: "shutting_down"})
			return
		}
		_ = json.NewEncoder(conn).Encode(response{OK: true, Status: "queued"})
	case "status":
		status, err := ctl.Status(ctx)
		if err != nil {
			_ = json.NewEncoder(conn).Encode(response{OK: false, Error: "shutting_down"})
			return
		}
		_ = json.NewEncoder(conn).Encode(response{
			OK:        true,
			State:     status.State,
			PID:       status.PID,
			AppPort:   status.AppPort,
			ProxyPort: status.ProxyPort,
			Version:   status.Version,
		})
	default:
		_ = json.NewEncoder(conn).Encode(response{OK: false, Error: "invalid_request"})
	}
}
