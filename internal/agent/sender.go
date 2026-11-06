package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/ckdqja135/MSP/internal/protocol"
)

func (a *Agent) authorize(request *http.Request) {
	request.Header.Set("X-MSP-Server", a.cfg.ServerID)
	request.Header.Set("Authorization", "Bearer "+a.cfg.Token)
}

func (a *Agent) post(ctx context.Context, batch protocol.Batch) error {
	body, err := json.Marshal(batch)
	if err != nil {
		return err
	}
	url := strings.TrimRight(a.cfg.CentralURL, "/") + "/agent/v1/batches"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	a.authorize(request)

	response, err := a.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		return fmt.Errorf("중앙 서버 응답 %d: %s", response.StatusCode, strings.TrimSpace(string(message)))
	}
	var ack protocol.Ack
	if err := json.NewDecoder(response.Body).Decode(&ack); err != nil {
		return fmt.Errorf("응답을 해석하지 못했습니다: %w", err)
	}
	if ack.Seq != batch.Seq {
		return fmt.Errorf("다른 순번의 응답입니다: %d != %d", ack.Seq, batch.Seq)
	}
	return nil
}
