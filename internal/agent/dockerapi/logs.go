package dockerapi

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// 한 줄의 최대 길이다. 길이 제한 없는 줄이 메모리를 채우지 않게 한다.
const maxLogLineBytes = 16 << 10

// ErrLogsUnavailable은 로그 드라이버가 읽기를 지원하지 않는 경우다.
var ErrLogsUnavailable = errors.New("이 컨테이너의 로그 드라이버는 읽기를 지원하지 않습니다")

type LogOptions struct {
	Since  time.Time
	Until  time.Time
	Tail   int
	Follow bool
}

type LogEntry struct {
	At     time.Time
	Stream string
	Text   string
}

// Logs는 Docker에 남아 있는 로그를 읽는다. emit이 false를 돌려주면 읽기를 멈춘다.
func (c *Client) Logs(ctx context.Context, id string, options LogOptions, emit func(LogEntry) bool) error {
	var info struct {
		Config struct {
			Tty bool `json:"Tty"`
		} `json:"Config"`
	}
	if err := c.getJSON(ctx, "/containers/"+id+"/json", nil, &info); err != nil {
		return err
	}

	query := url.Values{"stdout": {"true"}, "stderr": {"true"}, "timestamps": {"true"}}
	if options.Tail > 0 {
		query.Set("tail", strconv.Itoa(options.Tail))
	}
	if !options.Since.IsZero() {
		query.Set("since", strconv.FormatInt(options.Since.Unix(), 10))
	}
	if !options.Until.IsZero() {
		query.Set("until", strconv.FormatInt(options.Until.Unix(), 10))
	}
	if options.Follow {
		query.Set("follow", "true")
	}
	resp, err := c.open(ctx, "/containers/"+id+"/logs", query)
	if err != nil {
		var status *StatusError
		if errors.As(err, &status) && status.Code == http.StatusNotImplemented {
			return ErrLogsUnavailable
		}
		return err
	}
	defer resp.Body.Close()
	if info.Config.Tty {
		return readRawLogs(resp.Body, emit)
	}
	return readMultiplexedLogs(resp.Body, emit)
}

func parseLogLine(stream, line string) LogEntry {
	line = strings.TrimRight(line, "\r\n")
	entry := LogEntry{Stream: stream, Text: line}
	if stamp, rest, found := strings.Cut(line, " "); found {
		if at, err := time.Parse(time.RFC3339Nano, stamp); err == nil {
			entry.At = at.UTC()
			entry.Text = rest
		}
	}
	return entry
}

func readRawLogs(body io.Reader, emit func(LogEntry) bool) error {
	reader := bufio.NewReaderSize(body, maxLogLineBytes)
	for {
		line, err := readLimitedLine(reader)
		if line != "" && !emit(parseLogLine("stdout", line)) {
			return nil
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

// readLimitedLine은 긴 줄을 잘라 읽고 나머지는 버린다.
func readLimitedLine(reader *bufio.Reader) (string, error) {
	part, isPrefix, err := reader.ReadLine()
	line := string(part)
	for isPrefix && err == nil {
		_, isPrefix, err = reader.ReadLine()
	}
	return line, err
}

// readMultiplexedLogs는 stdout과 stderr가 섞인 프레임을 읽는다. 8바이트 머리글 뒤에 내용이 온다.
// 프레임이 줄 중간에서 끊길 수 있어 스트림별로 남은 내용을 보관한다.
func readMultiplexedLogs(body io.Reader, emit func(LogEntry) bool) error {
	partial := map[string]string{}
	header := make([]byte, 8)
	flush := func() bool {
		for stream, rest := range partial {
			if rest != "" && !emit(parseLogLine(stream, rest)) {
				return false
			}
		}
		return true
	}
	for {
		if _, err := io.ReadFull(body, header); err != nil {
			flush()
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil
			}
			return err
		}
		stream := "stdout"
		if header[0] == 2 {
			stream = "stderr"
		}
		size := binary.BigEndian.Uint32(header[4:8])
		keep := size
		if keep > maxLogLineBytes {
			keep = maxLogLineBytes
		}
		payload := make([]byte, keep)
		if _, err := io.ReadFull(body, payload); err != nil {
			flush()
			return nil
		}
		if size > keep {
			if _, err := io.CopyN(io.Discard, body, int64(size-keep)); err != nil {
				flush()
				return nil
			}
		}
		text := partial[stream] + string(payload)
		for {
			line, rest, found := strings.Cut(text, "\n")
			if !found {
				break
			}
			if !emit(parseLogLine(stream, line)) {
				return nil
			}
			text = rest
		}
		if len(text) > maxLogLineBytes {
			text = text[:maxLogLineBytes]
		}
		partial[stream] = text
	}
}
