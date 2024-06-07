package headers

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"nvr/pkg/video/gortsplib/pkg/base"
	"strconv"
	"strings"
)

// TransportMode is a transport mode.
type TransportMode int

const (
	// TransportModePlay is the "play" transport mode.
	TransportModePlay TransportMode = iota

	// TransportModeRecord is the "record" transport mode.
	TransportModeRecord
)

// Transport is a Transport header.
type Transport struct {
	// (optional) interleaved frame ids
	InterleavedIDs *[2]int

	// (optional) SSRC of the packets of the stream
	SSRC *uint32

	// (optional) mode
	Mode *TransportMode
}

// ErrPortsInvalid invalid ports.
var ErrPortsInvalid = errors.New("invalid ports")

func parsePorts(val string) (*[2]int, error) {
	ports := strings.Split(val, "-")
	if len(ports) == 2 {
		port1, err := strconv.ParseInt(ports[0], 10, 64)
		if err != nil {
			return &[2]int{0, 0}, fmt.Errorf("%w (%v)", ErrPortsInvalid, val)
		}

		port2, err := strconv.ParseInt(ports[1], 10, 64)
		if err != nil {
			return &[2]int{0, 0}, fmt.Errorf("%w (%v)", ErrPortsInvalid, val)
		}

		return &[2]int{int(port1), int(port2)}, nil
	}

	if len(ports) == 1 {
		port1, err := strconv.ParseInt(ports[0], 10, 64)
		if err != nil {
			return &[2]int{0, 0}, fmt.Errorf("%w (%v)", ErrPortsInvalid, val)
		}

		return &[2]int{int(port1), int(port1 + 1)}, nil
	}

	return &[2]int{0, 0}, fmt.Errorf("%w (%v)", ErrPortsInvalid, val)
}

// Transport errors.
var (
	ErrTransportValueMissing     = errors.New("value not provided")
	ErrTransportMultipleValues   = errors.New("value provided multiple times")
	ErrTransportInvalidMode      = errors.New("invalid transport mode")
	ErrTransportProtocolNotFound = errors.New("protocol not found")
)

// Unmarshal decodes a Transport header.
func (h *Transport) Unmarshal(v base.HeaderValue) error { //nolint:funlen
	if len(v) == 0 {
		return ErrTransportValueMissing
	}

	if len(v) > 1 {
		return fmt.Errorf("%w (%v)", ErrTransportMultipleValues, v)
	}

	v0 := v[0]

	kvs, err := keyValParse(v0, ';')
	if err != nil {
		return err
	}

	protocolFound := false

	for k, rv := range kvs {
		v := rv

		switch k {
		case "RTP/AVP/TCP":
			protocolFound = true

		case "interleaved":
			ports, err := parsePorts(v)
			if err != nil {
				return err
			}
			h.InterleavedIDs = ports

		case "ssrc":
			v = strings.TrimLeft(v, " ")

			if (len(v) % 2) != 0 {
				v = "0" + v
			}

			if tmp, err := hex.DecodeString(v); err == nil && len(tmp) <= 4 {
				var ssrc [4]byte
				copy(ssrc[4-len(tmp):], tmp)
				v := uint32(ssrc[0])<<24 | uint32(ssrc[1])<<16 | uint32(ssrc[2])<<8 | uint32(ssrc[3])
				h.SSRC = &v
			}

		case "mode":
			str := strings.ToLower(v)
			str = strings.TrimPrefix(str, "\"")
			str = strings.TrimSuffix(str, "\"")

			switch str {
			case "play":
				v := TransportModePlay
				h.Mode = &v

				// receive is an old alias for record, used by ffmpeg with the
				// -listen flag, and by Darwin Streaming Server
			case "record", "receive":
				v := TransportModeRecord
				h.Mode = &v

			default:
				return fmt.Errorf("%w: '%s'", ErrTransportInvalidMode, str)
			}

		default:
			// ignore non-standard keys
		}
	}

	if !protocolFound {
		return fmt.Errorf("%w (%v)", ErrTransportProtocolNotFound, v[0])
	}

	return nil
}

// Marshal encodes a Transport header.
func (h Transport) Marshal() base.HeaderValue {
	var rets []string

	rets = append(rets, "RTP/AVP/TCP")

	if h.InterleavedIDs != nil {
		rets = append(rets, "interleaved="+strconv.FormatInt(int64(h.InterleavedIDs[0]), 10)+
			"-"+strconv.FormatInt(int64(h.InterleavedIDs[1]), 10))
	}

	if h.SSRC != nil {
		tmp := make([]byte, 4)
		binary.BigEndian.PutUint32(tmp, *h.SSRC)
		rets = append(rets, "ssrc="+strings.ToUpper(hex.EncodeToString(tmp)))
	}

	if h.Mode != nil {
		if *h.Mode == TransportModePlay {
			rets = append(rets, "mode=play")
		} else {
			rets = append(rets, "mode=record")
		}
	}

	return base.HeaderValue{strings.Join(rets, ";")}
}
