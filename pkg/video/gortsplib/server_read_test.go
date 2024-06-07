package gortsplib

import (
	"net"
	"testing"
	"time"

	"nvr/pkg/video/gortsplib/pkg/base"
	"nvr/pkg/video/gortsplib/pkg/conn"
	"nvr/pkg/video/gortsplib/pkg/headers"

	"github.com/stretchr/testify/require"
)

func getIP(t *testing.T) string {
	intfs, err := net.Interfaces()
	require.NoError(t, err)

	for _, intf := range intfs {
		addrs, err := intf.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			switch v := addr.(type) {
			case *net.IPNet:
				return v.IP.String()
			case *net.IPAddr:
				return v.IP.String()
			}
		}
	}

	t.Errorf("unable to find a IP")
	return ""
}

func TestServerReadSetupPath(t *testing.T) {
	for _, ca := range []struct {
		name    string
		url     string
		path    string
		trackID int
	}{
		{
			"normal",
			"rtsp://localhost:8554/teststream/trackID=2",
			"teststream",
			2,
		},
		{
			// this is needed to support reading mpegts with ffmpeg
			"without track id",
			"rtsp://localhost:8554/teststream/",
			"teststream",
			0,
		},
		{
			"subpath",
			"rtsp://localhost:8554/test/stream/trackID=0",
			"test/stream",
			0,
		},
		{
			"subpath without track id",
			"rtsp://localhost:8554/test/stream/",
			"test/stream",
			0,
		},
	} {
		t.Run(ca.name, func(t *testing.T) {
			track := &TrackH264{
				PayloadType: 96,
				SPS:         []byte{0x01, 0x02, 0x03, 0x04},
				PPS:         []byte{0x01, 0x02, 0x03, 0x04},
			}

			stream := NewServerStream(Tracks{track, track, track, track, track})
			defer stream.Close()

			s := &Server{
				handler: &testServerHandler{
					onSetup: func(
						_ *ServerSession,
						path string,
						trackID int,
					) (*base.Response, *ServerStream, error) {
						require.Equal(t, ca.path, path)
						require.Equal(t, ca.trackID, trackID)
						return &base.Response{
							StatusCode: base.StatusOK,
						}, stream, nil
					},
				},
				rtspAddress: "localhost:8554",
			}

			err := s.Start()
			require.NoError(t, err)
			defer s.Close()

			nconn, err := net.Dial("tcp", "localhost:8554")
			require.NoError(t, err)
			defer nconn.Close()
			conn := conn.NewConn(nconn)

			th := &headers.Transport{
				Mode: func() *headers.TransportMode {
					v := headers.TransportModePlay
					return &v
				}(),
				InterleavedIDs: &[2]int{ca.trackID * 2, (ca.trackID * 2) + 1},
			}

			res, err := writeReqReadRes(conn, base.Request{
				Method: base.Setup,
				URL:    mustParseURL(ca.url),
				Header: base.Header{
					"CSeq":      base.HeaderValue{"1"},
					"Transport": th.Marshal(),
				},
			})
			require.NoError(t, err)
			require.Equal(t, base.StatusOK, res.StatusCode)
		})
	}
}

func TestServerReadSetupErrors(t *testing.T) {
	for _, ca := range []string{
		"different paths",
		"double setup",
		"closed stream",
	} {
		t.Run(ca, func(t *testing.T) {
			connClosed := make(chan struct{})

			track := &TrackH264{
				PayloadType: 96,
				SPS:         []byte{0x01, 0x02, 0x03, 0x04},
				PPS:         []byte{0x01, 0x02, 0x03, 0x04},
			}

			stream := NewServerStream(Tracks{track})
			if ca == "closed stream" {
				stream.Close()
			} else {
				defer stream.Close()
			}

			s := &Server{
				handler: &testServerHandler{
					onConnClose: func(_ *ServerConn, err error) {
						switch ca {
						case "different paths":
							require.EqualError(t, err, "read: can't setup tracks with different paths")

						case "double setup":
							require.EqualError(t, err, "read: track 0 has already been setup")

						case "closed stream":
							require.EqualError(t, err, "read: stream is closed")
						}
						close(connClosed)
					},
					onSetup: func(*ServerSession, string, int) (*base.Response, *ServerStream, error) {
						return &base.Response{
							StatusCode: base.StatusOK,
						}, stream, nil
					},
				},
				rtspAddress: "localhost:8554",
			}

			err := s.Start()
			require.NoError(t, err)
			defer s.Close()

			nconn, err := net.Dial("tcp", "localhost:8554")
			require.NoError(t, err)
			defer nconn.Close()
			conn := conn.NewConn(nconn)

			th := &headers.Transport{
				Mode: func() *headers.TransportMode {
					v := headers.TransportModePlay
					return &v
				}(),
				InterleavedIDs: &[2]int{0, 1},
			}

			res, err := writeReqReadRes(conn, base.Request{
				Method: base.Setup,
				URL:    mustParseURL("rtsp://localhost:8554/teststream/trackID=0"),
				Header: base.Header{
					"CSeq":      base.HeaderValue{"1"},
					"Transport": th.Marshal(),
				},
			})

			switch ca {
			case "different paths":
				require.NoError(t, err)
				require.Equal(t, base.StatusOK, res.StatusCode)

				var sx headers.Session
				err = sx.Unmarshal(res.Header["Session"])
				require.NoError(t, err)
				th.InterleavedIDs = &[2]int{2, 3}

				res, err = writeReqReadRes(conn, base.Request{
					Method: base.Setup,
					URL:    mustParseURL("rtsp://localhost:8554/test12stream/trackID=1"),
					Header: base.Header{
						"CSeq":      base.HeaderValue{"2"},
						"Transport": th.Marshal(),
						"Session":   base.HeaderValue{sx.Session},
					},
				})
				require.NoError(t, err)
				require.Equal(t, base.StatusBadRequest, res.StatusCode)

			case "double setup":
				require.NoError(t, err)
				require.Equal(t, base.StatusOK, res.StatusCode)

				var sx headers.Session
				err = sx.Unmarshal(res.Header["Session"])
				require.NoError(t, err)
				th.InterleavedIDs = &[2]int{2, 3}

				res, err = writeReqReadRes(conn, base.Request{
					Method: base.Setup,
					URL:    mustParseURL("rtsp://localhost:8554/teststream/trackID=0"),
					Header: base.Header{
						"CSeq":      base.HeaderValue{"2"},
						"Transport": th.Marshal(),
						"Session":   base.HeaderValue{sx.Session},
					},
				})
				require.NoError(t, err)
				require.Equal(t, base.StatusBadRequest, res.StatusCode)

			case "closed stream":
				require.NoError(t, err)
				require.Equal(t, base.StatusBadRequest, res.StatusCode)
			}

			<-connClosed
		})
	}
}

func TestServerReadTCPResponseBeforeFrames(t *testing.T) {
	writerDone := make(chan struct{})
	writerTerminate := make(chan struct{})

	track := &TrackH264{
		PayloadType: 96,
		SPS:         []byte{0x01, 0x02, 0x03, 0x04},
		PPS:         []byte{0x01, 0x02, 0x03, 0x04},
	}

	stream := NewServerStream(Tracks{track})
	defer stream.Close()

	s := &Server{
		rtspAddress: "localhost:8554",
		handler: &testServerHandler{
			onConnClose: func(*ServerConn, error) {
				close(writerTerminate)
				<-writerDone
			},
			onSetup: func(*ServerSession, string, int) (*base.Response, *ServerStream, error) {
				return &base.Response{
					StatusCode: base.StatusOK,
				}, stream, nil
			},
			onPlay: func(*ServerSession) (*base.Response, error) {
				go func() {
					defer close(writerDone)

					stream.WritePacketRTP(0, &testRTPPacket)

					t := time.NewTicker(50 * time.Millisecond)
					defer t.Stop()

					for {
						select {
						case <-t.C:
							stream.WritePacketRTP(0, &testRTPPacket)
						case <-writerTerminate:
							return
						}
					}
				}()

				time.Sleep(50 * time.Millisecond)

				return &base.Response{
					StatusCode: base.StatusOK,
				}, nil
			},
		},
	}

	err := s.Start()
	require.NoError(t, err)
	defer s.Close()

	nconn, err := net.Dial("tcp", "localhost:8554")
	require.NoError(t, err)
	defer nconn.Close()
	conn := conn.NewConn(nconn)

	res, err := writeReqReadRes(conn, base.Request{
		Method: base.Setup,
		URL:    mustParseURL("rtsp://localhost:8554/teststream/trackID=0"),
		Header: base.Header{
			"CSeq": base.HeaderValue{"1"},
			"Transport": headers.Transport{
				Mode: func() *headers.TransportMode {
					v := headers.TransportModePlay
					return &v
				}(),
				InterleavedIDs: &[2]int{0, 1},
			}.Marshal(),
		},
	})
	require.NoError(t, err)
	require.Equal(t, base.StatusOK, res.StatusCode)

	var sx headers.Session
	err = sx.Unmarshal(res.Header["Session"])
	require.NoError(t, err)

	res, err = writeReqReadRes(conn, base.Request{
		Method: base.Play,
		URL:    mustParseURL("rtsp://localhost:8554/teststream"),
		Header: base.Header{
			"CSeq":    base.HeaderValue{"2"},
			"Session": base.HeaderValue{sx.Session},
		},
	})
	require.NoError(t, err)
	require.Equal(t, base.StatusOK, res.StatusCode)

	_, err = conn.ReadInterleavedFrame()
	require.NoError(t, err)
}

func TestServerReadWithoutTeardown(t *testing.T) {
	connClosed := make(chan struct{})
	sessionClosed := make(chan struct{})

	track := &TrackH264{
		PayloadType: 96,
		SPS:         []byte{0x01, 0x02, 0x03, 0x04},
		PPS:         []byte{0x01, 0x02, 0x03, 0x04},
	}

	stream := NewServerStream(Tracks{track})
	defer stream.Close()

	s := &Server{
		handler: &testServerHandler{
			onConnClose: func(*ServerConn, error) {
				close(connClosed)
			},
			onSessionClose: func(*ServerSession, error) {
				close(sessionClosed)
			},
			onAnnounce: func(*ServerSession, string, Tracks) (*base.Response, error) {
				return &base.Response{StatusCode: base.StatusOK}, nil
			},
			onSetup: func(*ServerSession, string, int) (*base.Response, *ServerStream, error) {
				return &base.Response{StatusCode: base.StatusOK}, stream, nil
			},
			onPlay: func(*ServerSession) (*base.Response, error) {
				return &base.Response{StatusCode: base.StatusOK}, nil
			},
		},
		readTimeout:    1 * time.Second,
		sessionTimeout: 1 * time.Second,
		rtspAddress:    "localhost:8554",
	}

	err := s.Start()
	require.NoError(t, err)
	defer s.Close()

	nconn, err := net.Dial("tcp", "localhost:8554")
	require.NoError(t, err)
	defer nconn.Close()
	conn := conn.NewConn(nconn)

	inTH := &headers.Transport{
		Mode: func() *headers.TransportMode {
			v := headers.TransportModePlay
			return &v
		}(),
	}

	inTH.InterleavedIDs = &[2]int{0, 1}

	res, err := writeReqReadRes(conn, base.Request{
		Method: base.Setup,
		URL:    mustParseURL("rtsp://localhost:8554/teststream/trackID=0"),
		Header: base.Header{
			"CSeq":      base.HeaderValue{"1"},
			"Transport": inTH.Marshal(),
		},
	})
	require.NoError(t, err)
	require.Equal(t, base.StatusOK, res.StatusCode)

	var sx headers.Session
	err = sx.Unmarshal(res.Header["Session"])
	require.NoError(t, err)

	res, err = writeReqReadRes(conn, base.Request{
		Method: base.Play,
		URL:    mustParseURL("rtsp://localhost:8554/teststream"),
		Header: base.Header{
			"CSeq":    base.HeaderValue{"2"},
			"Session": base.HeaderValue{sx.Session},
		},
	})
	require.NoError(t, err)
	require.Equal(t, base.StatusOK, res.StatusCode)

	nconn.Close()

	<-sessionClosed
	<-connClosed
}
