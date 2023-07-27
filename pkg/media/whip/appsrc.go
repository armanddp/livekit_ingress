// Copyright 2023 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package whip

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/frostbyte73/core"
	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/types"
	"github.com/livekit/ingress/pkg/utils"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/tracer"
	"github.com/pion/webrtc/v3"
	"github.com/tinyzimmer/go-gst/gst"
	"github.com/tinyzimmer/go-gst/gst/app"
)

type whipAppSource struct {
	appSrc     *app.Source
	trackKind  types.StreamKind
	relayUrl   string
	resourceId string

	fuse   core.Fuse
	result chan error
}

func NewWHIPAppSource(ctx context.Context, resourceId string, trackKind types.StreamKind, mimeType string, relayUrl string) (*whipAppSource, error) {
	ctx, span := tracer.Start(ctx, "WHIPRelaySource.New")
	defer span.End()

	w := &whipAppSource{
		trackKind:  trackKind,
		relayUrl:   relayUrl,
		resourceId: resourceId,
		fuse:       core.NewFuse(),
		result:     make(chan error, 1),
	}

	elem, err := gst.NewElementWithName("appsrc", fmt.Sprintf("%s_%s", WHIPAppSourceLabel, trackKind))
	if err != nil {
		logger.Errorw("could not create appsrc", err, "resourceID", w.resourceId, "kind", w.trackKind)
		return nil, err
	}
	caps, err := getCapsForCodec(mimeType)
	if err != nil {
		return nil, err
	}
	if err = elem.SetProperty("caps", caps); err != nil {
		return nil, err
	}
	if err = elem.SetProperty("is-live", true); err != nil {
		return nil, err
	}
	elem.SetArg("format", "time")

	w.appSrc = app.SrcFromElement(elem)

	return w, nil
}

func (w *whipAppSource) Start(ctx context.Context) error {
	ctx, span := tracer.Start(ctx, "RTMPRelaySource.Start")
	defer span.End()

	logger.Debugw("starting WHIP app source", "resourceID", w.resourceId, "kind", w.trackKind)

	resp, err := http.Get(w.relayUrl)
	switch {
	case err != nil:
		return err
	case resp != nil && (resp.StatusCode < 200 || resp.StatusCode >= 400):
		return errors.ErrHttpRelayFailure(resp.StatusCode)
	}

	go func() {
		defer resp.Body.Close()

		err := w.copyRelayedData(resp.Body)
		logger.Debugw("WHIP app source relay stopped", "error", err, "resourceID", w.resourceId, "kind", w.trackKind)

		w.appSrc.EndStream()

		w.result <- err
		close(w.result)
	}()

	return nil
}

func (w *whipAppSource) Close() <-chan error {
	logger.Debugw("WHIP app source relay Close called", "resourceID", w.resourceId, "kind", w.trackKind)
	w.fuse.Break()

	return w.result
}

func (w *whipAppSource) GetAppSource() *app.Source {
	return w.appSrc
}

func (w *whipAppSource) copyRelayedData(r io.Reader) error {
	for {
		if w.fuse.IsBroken() {
			return io.EOF
		}

		data, ts, err := utils.DeserializeMediaForRelay(r)
		switch err {
		case nil:
			// continue
		case io.EOF:
			if w.fuse.IsBroken() {
				// client closed the peer connection at the same time as it sent the DELETE request
				return io.EOF
			} else {
				// relay stopped without a clean session shutdown
				return io.ErrUnexpectedEOF
			}
		default:
			return err
		}

		b := gst.NewBufferFromBytes(data)
		b.SetPresentationTimestamp(ts)

		ret := w.appSrc.PushBuffer(b)
		switch ret {
		case gst.FlowOK, gst.FlowFlushing:
			// continue
		case gst.FlowEOS:
			return io.EOF
		default:
			return errors.ErrFromGstFlowReturn(ret)
		}
	}
}

func getCapsForCodec(mimeType string) (*gst.Caps, error) {
	mt := strings.ToLower(mimeType)

	switch mt {
	case strings.ToLower(webrtc.MimeTypeH264):
		return gst.NewCapsFromString("video/x-h264,stream-format=byte-stream,alignment=nal"), nil
	case strings.ToLower(webrtc.MimeTypeVP8):
		return gst.NewCapsFromString("video/x-vp8"), nil
	case strings.ToLower(webrtc.MimeTypeOpus):
		return gst.NewCapsFromString("audio/x-opus,channel-mapping-family=0"), nil
	}

	return nil, errors.ErrUnsupportedDecodeFormat
}
