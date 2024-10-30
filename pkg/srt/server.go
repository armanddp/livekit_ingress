package srt

import (
	"fmt"
	"io"
	"net/url"
	"strings"
	"sync"

	"github.com/haivision/srtgo"

	"github.com/livekit/protocol/logger"
)

// SRTServer wraps the SRT functionality for ingress
type SRTServer struct {
	socket   *srtgo.SrtSocket
	done     chan struct{}
	onStream func(streamKey string, r io.ReadCloser) error
	mu       sync.RWMutex
}

func NewSRTServer() *SRTServer {
	return &SRTServer{
		done: make(chan struct{}),
	}
}

func (s *SRTServer) OnStream(cb func(streamKey string, r io.ReadCloser) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onStream = cb
}

func (s *SRTServer) Start(port int) error {
	options := map[string]string{
		"blocking":  "0",    // Non-blocking mode
		"transtype": "live", // Live streaming mode
		"latency":   "120",  // Default latency in ms
	}

	s.socket = srtgo.NewSrtSocket("0.0.0.0", uint16(port), options)
	if s.socket == nil {
		return fmt.Errorf("failed to create SRT socket")
	}

	if err := s.socket.Listen(1); err != nil {
		return fmt.Errorf("failed to start SRT server: %w", err)
	}

	go s.acceptLoop()

	return nil
}

func (s *SRTServer) Stop() error {
	close(s.done)
	if s.socket != nil {
		s.socket.Close()
	}
	return nil
}

func (s *SRTServer) acceptLoop() {
	for {
		select {
		case <-s.done:
			return
		default:
			socket, addr, err := s.socket.Accept()
			if err != nil {
				logger.Errorw("failed to accept SRT connection", err)
				continue
			}
			logger.Debugw("accepted SRT connection", "addr", addr)

			go s.handleConnection(socket)
		}
	}
}

func (s *SRTServer) handleConnection(socket *srtgo.SrtSocket) {
	// Get streamid from socket options
	streamid, err := socket.GetSockOptString(srtgo.SRTO_STREAMID)
	if err != nil {
		logger.Errorw("failed to get streamid", err)
		socket.Close()
		return
	}

	streamKey := extractStreamKey(streamid)
	if streamKey == "" {
		logger.Errorw("missing stream key in SRT connection", fmt.Errorf("invalid streamid"), "streamid", streamid)
		socket.Close()
		return
	}

	s.mu.RLock()
	onStream := s.onStream
	s.mu.RUnlock()

	if onStream != nil {
		stream := newSRTStream(socket)
		if err := onStream(streamKey, stream); err != nil {
			logger.Errorw("stream handler failed", err)
			socket.Close()
			return
		}
	}
}

// SRTStream implements io.ReadCloser for SRT connections
type SRTStream struct {
	socket *srtgo.SrtSocket
}

func newSRTStream(socket *srtgo.SrtSocket) *SRTStream {
	return &SRTStream{socket: socket}
}

func (s *SRTStream) Read(p []byte) (n int, err error) {
	n, err = s.socket.Read(p)
	if err != nil {
		return 0, err
	}
	return n, nil
}

func (s *SRTStream) Close() error {
	s.socket.Close()
	return nil
}

// extractStreamKey parses the streamid from query string format
// Example: streamid=channel1
func extractStreamKey(streamid string) string {
	values, err := url.ParseQuery(streamid)
	if err != nil {
		return strings.TrimSpace(streamid)
	}

	key := values.Get("streamid")
	if key == "" {
		key = strings.TrimSpace(streamid)
	}

	return key
}
