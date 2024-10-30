package srt

import (
	"io"
	"net"
	"sync"

	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/ingress/pkg/stats"
	"github.com/livekit/protocol/logger"
)

type SRTHandler struct {
	conn       net.Conn
	params     *params.Params
	stats      *stats.LocalMediaStatsGatherer
	writer     io.WriteCloser
	writerLock sync.Mutex
	closed     bool
}

func NewSRTHandler(conn net.Conn) *SRTHandler {
	return &SRTHandler{
		conn: conn,
	}
}

func (h *SRTHandler) Start(params *params.Params, stats *stats.LocalMediaStatsGatherer) {
	h.params = params
	h.stats = stats

	go h.readLoop()
}

func (h *SRTHandler) SetWriter(w io.WriteCloser, token string) error {
	h.writerLock.Lock()
	defer h.writerLock.Unlock()

	if h.closed {
		return errors.ErrIngressNotFound
	}

	if h.params != nil && token != h.params.RelayToken {
		return errors.ErrInvalidRelayToken
	}

	if h.writer != nil {
		h.writer.Close()
	}
	h.writer = w

	return nil
}

func (h *SRTHandler) Close() {
	h.writerLock.Lock()
	defer h.writerLock.Unlock()

	if !h.closed {
		h.closed = true
		h.conn.Close()
		if h.writer != nil {
			h.writer.Close()
		}
	}
}

func (h *SRTHandler) readLoop() {
	buf := make([]byte, 4096)
	for {
		n, err := h.conn.Read(buf)
		if err != nil {
			if err != io.EOF {
				logger.Errorw("SRT read error", err)
			}
			h.Close()
			return
		}

		h.writerLock.Lock()
		if h.writer != nil {
			_, err = h.writer.Write(buf[:n])
			if err != nil {
				logger.Errorw("relay write error", err)
				h.writerLock.Unlock()
				h.Close()
				return
			}
		}
		h.writerLock.Unlock()
	}
}
