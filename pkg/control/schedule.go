package control

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// ParseShutdownTime reads shutdown(8)'s time operand: "now" (or empty),
// "+N" or a bare "N" minutes, or "HH:MM" — today, or tomorrow when that
// time has passed. slinitctl shutdown and the shutdown command share it.
func ParseShutdownTime(s string, now time.Time) (time.Duration, error) {
	if s == "now" || s == "" {
		return 0, nil
	}
	if strings.HasPrefix(s, "+") {
		mins, err := strconv.Atoi(s[1:])
		if err != nil || mins < 0 {
			return 0, fmt.Errorf("invalid time: %s (use +N for minutes)", s)
		}
		return time.Duration(mins) * time.Minute, nil
	}
	if mins, err := strconv.Atoi(s); err == nil && mins >= 0 {
		return time.Duration(mins) * time.Minute, nil
	}
	if parts := strings.SplitN(s, ":", 2); len(parts) == 2 {
		h, err1 := strconv.Atoi(parts[0])
		m, err2 := strconv.Atoi(parts[1])
		if err1 == nil && err2 == nil && h >= 0 && h <= 23 && m >= 0 && m <= 59 {
			target := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location())
			if target.Before(now) {
				target = target.Add(24 * time.Hour)
			}
			return target.Sub(now), nil
		}
	}
	return 0, fmt.Errorf("invalid time: %s (use 'now', '+N' for minutes, or 'HH:MM')", s)
}

// EncodeScheduleShutdown builds CmdScheduleShutdown's payload:
//
//	[type(1)] [delay_secs(4, big-endian)] [msg_len(2, LE)] [msg_bytes...]
//
// The message tail is only appended when non-empty, so a daemon from
// before messages existed still accepts the packet.
func EncodeScheduleShutdown(st service.ShutdownType, delay time.Duration, message string) []byte {
	secs := uint32(delay / time.Second)
	payload := []byte{
		uint8(st),
		byte(secs >> 24), byte(secs >> 16), byte(secs >> 8), byte(secs),
	}
	if message != "" {
		msg := []byte(message)
		if len(msg) > 0xFFFF {
			msg = msg[:0xFFFF]
		}
		payload = append(payload, byte(len(msg)&0xFF), byte((len(msg)>>8)&0xFF))
		payload = append(payload, msg...)
	}
	return payload
}

// DecodeScheduleShutdown is EncodeScheduleShutdown's inverse. ok is false
// for a payload too short to carry the fixed part or naming no valid
// shutdown type; a truncated message reads as none.
func DecodeScheduleShutdown(payload []byte) (st service.ShutdownType, delay time.Duration, message string, ok bool) {
	if len(payload) < 5 {
		return 0, 0, "", false
	}
	st = service.ShutdownType(payload[0])
	if !validShutdownType(st) {
		return 0, 0, "", false
	}
	secs := uint32(payload[1])<<24 | uint32(payload[2])<<16 | uint32(payload[3])<<8 | uint32(payload[4])
	delay = time.Duration(secs) * time.Second
	if len(payload) >= 7 {
		n := int(payload[5]) | int(payload[6])<<8
		if n > 0 && 7+n <= len(payload) {
			message = string(payload[7 : 7+n])
		}
	}
	return st, delay, message, true
}
