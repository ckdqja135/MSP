package agent

import (
	"context"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/sensors"
)

// HostReader는 호스트 지표를 읽는다. 항목별로 실패할 수 있어 오류를 따로 돌려준다.
type HostReader interface {
	CPUPercent(ctx context.Context) (float64, error)
	Memory(ctx context.Context) (percent float64, used uint64, err error)
	DiskUsage(ctx context.Context) (percent float64, inodePercent float64, err error)
	DiskIO(ctx context.Context) (read, write uint64, err error)
	Network(ctx context.Context) (rx, tx uint64, err error)
	// Temperature의 supported가 false면 이 환경에서 수집할 수 없다는 뜻이다.
	Temperature(ctx context.Context) (celsius float64, supported bool, err error)
	Boot(ctx context.Context) (bootTime time.Time, uptimeSeconds int64, err error)
}

// SystemHost는 실제 호스트 값을 읽는 구현이다.
type SystemHost struct {
	DiskPath string
}

func (h SystemHost) CPUPercent(ctx context.Context) (float64, error) {
	// 간격 0은 직전 호출 이후의 평균이다. 첫 호출은 부팅 이후 평균에 가깝다.
	values, err := cpu.PercentWithContext(ctx, 0, false)
	if err != nil || len(values) == 0 {
		return 0, err
	}
	return values[0], nil
}

func (h SystemHost) Memory(ctx context.Context) (float64, uint64, error) {
	info, err := mem.VirtualMemoryWithContext(ctx)
	if err != nil {
		return 0, 0, err
	}
	return info.UsedPercent, info.Used, nil
}

func (h SystemHost) DiskUsage(ctx context.Context) (float64, float64, error) {
	info, err := disk.UsageWithContext(ctx, h.DiskPath)
	if err != nil {
		return 0, 0, err
	}
	return info.UsedPercent, info.InodesUsedPercent, nil
}

func (h SystemHost) DiskIO(ctx context.Context) (uint64, uint64, error) {
	counters, err := disk.IOCountersWithContext(ctx)
	if err != nil {
		return 0, 0, err
	}
	var read, write uint64
	for _, counter := range counters {
		read += counter.ReadBytes
		write += counter.WriteBytes
	}
	return read, write, nil
}

func (h SystemHost) Network(ctx context.Context) (uint64, uint64, error) {
	counters, err := net.IOCountersWithContext(ctx, false)
	if err != nil || len(counters) == 0 {
		return 0, 0, err
	}
	return counters[0].BytesRecv, counters[0].BytesSent, nil
}

func (h SystemHost) Temperature(ctx context.Context) (float64, bool, error) {
	values, err := sensors.TemperaturesWithContext(ctx)
	var highest float64
	for _, value := range values {
		if value.Temperature > highest {
			highest = value.Temperature
		}
	}
	// 센서가 없는 VM은 오류 또는 빈 목록을 돌려준다. 실패가 아니라 미지원으로 본다.
	if highest <= 0 {
		return 0, false, nil
	}
	return highest, true, err
}

func (h SystemHost) Boot(ctx context.Context) (time.Time, int64, error) {
	boot, err := host.BootTimeWithContext(ctx)
	if err != nil {
		return time.Time{}, 0, err
	}
	uptime, err := host.UptimeWithContext(ctx)
	if err != nil {
		return time.Time{}, 0, err
	}
	return time.Unix(int64(boot), 0).UTC(), int64(uptime), nil
}
