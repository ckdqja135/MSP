#!/bin/sh
# MSP가 서버에 주는 부하를 시작 이후 평균으로 보여 준다. 읽기만 한다.
# 디스크 기록량은 프로세스 소유자나 root만 읽을 수 있으므로 sudo로 실행하면 함께 나온다.
cpu_limit_percent=2
memory_limit_mb=150
cgroup=/sys/fs/cgroup/msp.slice
over=0
uptime_now=$(cut -d' ' -f1 /proc/uptime)

stat_of() { awk -v key="$2" '$1 == key {print $2}' "$1/memory.stat" 2>/dev/null; }

for name in msp-central msp-agent; do
    values=$(systemctl show "$name.service" -p ActiveState -p CPUUsageNSec -p MainPID -p NRestarts -p ActiveEnterTimestampMonotonic)
    get() { printf '%s\n' "$values" | sed -n "s/^$1=//p"; }
    state=$(get ActiveState)
    if [ "$state" != "active" ]; then
        echo "$name: $state"
        over=1
        continue
    fi
    pid=$(get MainPID)
    written=""
    [ -r "/proc/$pid/io" ] && written=$(awk '/^write_bytes/ {print $2}' "/proc/$pid/io")
    awk -v name="$name" -v now="$uptime_now" -v since="$(get ActiveEnterTimestampMonotonic)" -v cpu="$(get CPUUsageNSec)" \
        -v anon="$(stat_of "$cgroup/$name.service" anon)" -v restarts="$(get NRestarts)" -v written="$written" -v limit="$cpu_limit_percent" 'BEGIN {
        elapsed = now - since / 1000000
        percent = cpu / 1e9 / elapsed * 100
        line = sprintf("%s: 가동 %.1f시간, CPU 평균 %.2f%% (코어 1개 기준), 메모리 %.0fMB, 재시작 %d회", name, elapsed / 3600, percent, anon / 1048576, restarts)
        if (written != "") line = line sprintf(", 디스크 기록 %.1fKB/초", written / 1024 / elapsed)
        print line
        if (percent > limit) { printf "기준 초과: %s CPU 평균 %d%%\n", name, limit; exit 1 }
    }' || over=1
done

anon=$(stat_of "$cgroup" anon)
file=$(stat_of "$cgroup" file)
case "$anon$file" in
'' | *[!0-9]*) echo "메모리 값을 읽지 못했습니다"; over=1 ;;
*)
    anon_mb=$((anon / 1048576))
    echo "전체 메모리 ${anon_mb}MB, 파일 캐시 $((file / 1048576))MB (캐시는 필요하면 비워지며 상한 안에서만 쌓입니다)"
    [ "$anon_mb" -le "$memory_limit_mb" ] || { echo "기준 초과: 전체 메모리 ${memory_limit_mb}MB"; over=1; }
    ;;
esac

for path in /var/lib/msp-central /var/lib/msp-agent; do
    [ -r "$path" ] && du -sh "$path" 2>/dev/null
done

if [ "$over" -eq 0 ]; then
    echo "판정: 기준 안 (서비스별 CPU 평균 ${cpu_limit_percent}% 이하, 전체 메모리 ${memory_limit_mb}MB 이하)"
else
    echo "판정: 확인 필요. 멈추려면 sudo systemctl stop msp-agent msp-central"
    exit 1
fi
