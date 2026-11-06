package agent

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/ckdqja135/MSP/internal/protocol"
)

const spoolSuffix = ".batch"

// Spool은 전송 전 배치를 디스크에 보관한다. 중앙 서버가 끊겨도 데이터를 잃지 않기 위해서다.
// 상한을 넘으면 지표부터 버리고 이벤트를 남기며, 버린 내용은 공백 기록으로 남긴다.
type Spool struct {
	dir      string
	maxBytes int64
	mu       sync.Mutex
}

func OpenSpool(dir string, maxBytes int64) (*Spool, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Spool{dir: dir, maxBytes: maxBytes}, nil
}

func spoolName(startedAt int64, b protocol.Batch) string {
	return fmt.Sprintf("%020d-%s-%020d%s", startedAt, b.Epoch, b.Seq, spoolSuffix)
}

// Put은 배치를 원자적으로 저장하고 상한을 넘겨 버린 내용을 돌려준다.
func (s *Spool) Put(startedAt int64, b protocol.Batch) ([]protocol.Gap, error) {
	body, err := json.Marshal(b)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.write(spoolName(startedAt, b), body); err != nil {
		return nil, err
	}
	return s.enforce()
}

func (s *Spool) write(name string, body []byte) error {
	temp := filepath.Join(s.dir, name+".tmp")
	if err := os.WriteFile(temp, body, 0o600); err != nil {
		return err
	}
	return os.Rename(temp, filepath.Join(s.dir, name))
}

type spoolEntry struct {
	name string
	size int64
}

func (s *Spool) entries() ([]spoolEntry, int64, error) {
	listing, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, 0, err
	}
	var entries []spoolEntry
	var total int64
	for _, item := range listing {
		if !strings.HasSuffix(item.Name(), spoolSuffix) {
			continue
		}
		info, err := item.Info()
		if err != nil {
			continue
		}
		entries = append(entries, spoolEntry{item.Name(), info.Size()})
		total += info.Size()
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	return entries, total, nil
}

func (s *Spool) read(name string) (protocol.Batch, error) {
	var b protocol.Batch
	body, err := os.ReadFile(filepath.Join(s.dir, name))
	if err != nil {
		return b, err
	}
	return b, json.Unmarshal(body, &b)
}

// enforce는 오래된 지표부터 지우고, 그래도 넘으면 오래된 배치를 통째로 지운다.
func (s *Spool) enforce() ([]protocol.Gap, error) {
	entries, total, err := s.entries()
	if err != nil || total <= s.maxBytes {
		return nil, err
	}
	var gaps []protocol.Gap

	for i := range entries {
		if total <= s.maxBytes {
			return gaps, nil
		}
		b, err := s.read(entries[i].name)
		if err != nil || len(b.Samples) == 0 {
			continue
		}
		gaps = append(gaps, protocol.Gap{
			Source:  "spool",
			From:    b.Samples[0].At,
			To:      b.Samples[len(b.Samples)-1].At,
			Reason:  "전송 대기 공간이 부족해 지표를 버렸습니다",
			Dropped: len(b.Samples),
		})
		b.Samples = nil
		body, err := json.Marshal(b)
		if err != nil {
			return gaps, err
		}
		if err := s.write(entries[i].name, body); err != nil {
			return gaps, err
		}
		total += int64(len(body)) - entries[i].size
		entries[i].size = int64(len(body))
	}

	for _, entry := range entries {
		if total <= s.maxBytes {
			break
		}
		b, err := s.read(entry.name)
		if err == nil && len(b.Events) > 0 {
			gaps = append(gaps, protocol.Gap{
				Source:  "spool",
				From:    b.Events[0].OccurredAt,
				To:      b.Events[len(b.Events)-1].OccurredAt,
				Reason:  "전송 대기 공간이 부족해 이벤트를 버렸습니다",
				Dropped: len(b.Events),
			})
		}
		if err := os.Remove(filepath.Join(s.dir, entry.name)); err != nil {
			return gaps, err
		}
		total -= entry.size
	}
	return gaps, nil
}

// Oldest는 가장 오래된 배치를 돌려준다. 읽을 수 없는 파일은 지우고 다음으로 넘어간다.
func (s *Spool) Oldest() (string, protocol.Batch, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, _, err := s.entries()
	if err != nil {
		log.Printf("전송 대기 목록을 읽지 못했습니다: %v", err)
		return "", protocol.Batch{}, false
	}
	for _, entry := range entries {
		b, err := s.read(entry.name)
		if err == nil {
			return entry.name, b, true
		}
		log.Printf("손상된 전송 대기 파일을 지웁니다: %s", entry.name)
		os.Remove(filepath.Join(s.dir, entry.name))
	}
	return "", protocol.Batch{}, false
}

func (s *Spool) Remove(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return os.Remove(filepath.Join(s.dir, name))
}

// Count는 전송 대기 중인 배치 수다.
func (s *Spool) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, _, err := s.entries()
	if err != nil {
		return 0
	}
	return len(entries)
}
