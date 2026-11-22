package backup

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"time"

	"github.com/hirochachacha/go-smb2/v2/x/protocol"
)

func trySMB1(ctx context.Context, server string, modernErr error) bool {
	var invalid *protocol.InvalidResponseError
	var status *protocol.ResponseError
	// An authentication failure or a network outage provides no evidence
	// that the server requires the legacy dialect.
	const statusNotSupported = 0xc00000bb
	const statusInvalidParameter = 0xc000000d
	if !errors.Is(modernErr, io.EOF) && !errors.Is(modernErr, io.ErrUnexpectedEOF) &&
		!errors.As(modernErr, &invalid) &&
		!(errors.As(modernErr, &status) && (status.Code == statusNotSupported || status.Code == statusInvalidParameter)) {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, _, err := net.SplitHostPort(server); err != nil {
		server = net.JoinHostPort(server, "445")
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", server)
	if err != nil {
		return false
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	if err = conn.SetDeadline(deadline); err != nil {
		return false
	}
	return selectsSMB1(conn)
}

// A multi-protocol SMB_COM_NEGOTIATE probes dialects without any credentials.
// A modern response or a rejected negotiation never permits SMB1.
func selectsSMB1(conn io.ReadWriter) bool {
	const dialects = "\x02NT LM 0.12\x00\x02SMB 2.002\x00\x02SMB 2.???\x00"
	const smbHeader = 32
	const commandNegotiate = 0x72
	const ntResponseWords = 17
	const fixedResponse = smbHeader + 1 + 2*ntResponseWords + 2
	var request [4 + smbHeader + 3 + len(dialects)]byte
	binary.BigEndian.PutUint32(request[:4], uint32(len(request)-4))
	copy(request[4:], "\xffSMB")
	request[8] = commandNegotiate
	request[13] = 0x18
	binary.LittleEndian.PutUint16(request[14:16], 0x4801)
	binary.LittleEndian.PutUint16(request[37:39], uint16(len(dialects)))
	copy(request[39:], dialects)
	if n, err := conn.Write(request[:]); err != nil || n != len(request) {
		return false
	}
	var packet [4]byte
	if _, err := io.ReadFull(conn, packet[:]); err != nil || packet[0] != 0 ||
		binary.BigEndian.Uint32(packet[:]) < fixedResponse {
		return false
	}
	var response [fixedResponse]byte
	if _, err := io.ReadFull(conn, response[:]); err != nil {
		return false
	}
	return string(response[:4]) == "\xffSMB" && response[4] == commandNegotiate &&
		binary.LittleEndian.Uint32(response[5:9]) == 0 && response[9]&0x80 != 0 &&
		response[smbHeader] == ntResponseWords && binary.LittleEndian.Uint16(response[33:35]) == 0 &&
		binary.BigEndian.Uint32(packet[:]) == fixedResponse+uint32(binary.LittleEndian.Uint16(response[fixedResponse-2:]))
}
