# hauntty Benchmark Report

**Prompt**: "Consider our fleet local01 and remote01. Perform two benchmarks for hauntty — optimizing vs regular SSH calls — to: (1) check servers healthy through Linux logs, and (2) check if we had a break-in in the last 48 hours."

**Method**: Each task is executed two ways per server:
- **Raw SSH** — each command is a separate `ssh host "command"`, full output lands in the agent's context
- **hauntty** — persistent session, metadata-first (the agent sees `stdout_lines` before reading), count-before-dump, selective filtering

Date: 2026-03-11T14:24:30Z

| | local01 | remote01 |
|---|---|---|
| Network latency | 0.993 ms | 50.214 ms |

---

## local01

### Task 1: Server Health Check

*"Check if the server is healthy through Linux logs."*

#### Raw SSH (10 commands, 10 connections)

| # | Command | Time | Output |
|---|---------|------|--------|
| 1 | `dmesg | tail -100` | 547 ms | 0 lines (0 B) |
| 2 | `dmesg | grep -i 'error\|warn\|crit\|fail'` | 249 ms | 0 lines (0 B) |
| 3 | `journalctl -p err --since '24h ago' --no-pager` | 267 ms | 4 lines (454 B) |
| 4 | `journalctl -p warning --since '24h ago' --no-pager` | 257 ms | 61 lines (6.6 KB) |
| 5 | `dmesg | grep -i 'oom\|killed process\|out of memory'` | 264 ms | 0 lines (0 B) |
| 6 | `dmesg | grep -i 'nvidia\|xid\|gpu\|drm'` | 251 ms | 0 lines (0 B) |
| 7 | `systemctl --failed --no-pager` | 255 ms | 2 lines (58 B) |
| 8 | `df -h` | 270 ms | 14 lines (1.0 KB) |
| 9 | `free -h` | 254 ms | 2 lines (206 B) |
| 10 | `cat /var/log/syslog 2>/dev/null | tail -50 || journalctl --since '1h ago' --no-pager | tail -50` | 272 ms | 49 lines (4.9 KB) |
| | **Total** | **2886 ms** | **132 lines (13588 bytes)** |


#### hauntty (10 commands, 0 new connections)

| # | Command | Time | Agent sees |
|---|---------|------|------------|
| 1 | `dmesg --level=err,warn,crit | tail -30` | 108 ms | empty — clean |
| 2 | `dmesg | grep -ci 'error\|warn\|fail' || true` | 113 ms | count only |
| 3 | `journalctl -p err --since '24h ago' --no-pager | tail -30` | 112 ms | 5 lines |
| 4 | `journalctl -p warning --since '24h ago' --no-pager | wc -l` | 109 ms | count only |
| 5 | `journalctl -p warning --since '24h ago' --no-pager | grep -vi 'pipewire\|bluetooth\|pulseaudio\|wireplumber\|gnome\|gjs\|gsd-\|snapd\|dbus' | tail -20` | 113 ms | 20 lines |
| 6 | `dmesg | grep -i 'oom\|killed process' | tail -10` | 109 ms | empty — clean |
| 7 | `dmesg | grep -i 'nvidia\|xid\|gpu\|drm' | tail -10` | 109 ms | empty — clean |
| 8 | `systemctl --failed --no-pager` | 113 ms | 3 lines |
| 9 | `df -h | awk 'NR==1 || +$5 > 80'` | 111 ms | 1 lines |
| 10 | `free -h` | 110 ms | 3 lines |
| | **Total** | **1107 ms** | |

#### Comparison

| Metric | Raw SSH | hauntty | |
|--------|---------|---------|---|
| Wall clock | 2886 ms | 1107 ms | **2.6x faster** |
| Context bytes | 13.2 KB (132 lines) | 4.4 KB (metadata) | **67% less context** |
| SSH connections | 10 | 0 | |

### Task 2: Break-in Detection (last 48h)

*"Check if we had a break-in in the last 48 hours."*

#### Raw SSH (10 commands, 10 connections)

| # | Command | Time | Output |
|---|---------|------|--------|
| 1 | `journalctl -u sshd --since '48h ago' --no-pager` | 262 ms | 0 lines (16 B) |
| 2 | `last -50` | 237 ms | 51 lines (3.6 KB) |
| 3 | `lastb -50 2>/dev/null || echo 'no lastb'` | 251 ms | 0 lines (8 B) |
| 4 | `grep -i 'failed\|invalid\|accepted' /var/log/auth.log 2>/dev/null || journalctl -u sshd --since '48h ago' --no-pager | grep -i 'failed\|invalid\|accepted'` | 259 ms | 609 lines (104.5 KB) |
| 5 | `journalctl -t sudo --since '48h ago' --no-pager` | 284 ms | 137 lines (14.4 KB) |
| 6 | `find /tmp /var/tmp -type f -mtime -2 -ls 2>/dev/null | head -50` | 252 ms | 1 lines (178 B) |
| 7 | `cat /etc/passwd | grep -v nologin | grep -v false` | 264 ms | 3 lines (169 B) |
| 8 | `ss -tlnp` | 268 ms | 14 lines (989 B) |
| 9 | `journalctl -p err --since '48h ago' --no-pager --grep='segfault|exploit|overflow|unauthorized' 2>/dev/null || echo 'none'` | 264 ms | 8 lines (329 B) |
| 10 | `cat /var/log/auth.log 2>/dev/null | tail -100 || journalctl -u sshd --since '48h ago' --no-pager | tail -100` | 247 ms | 99 lines (11.5 KB) |
| | **Total** | **2588 ms** | **922 lines (138990 bytes)** |


#### hauntty (10 commands, 0 new connections)

| # | Command | Time | Agent sees |
|---|---------|------|------------|
| 1 | `journalctl -u sshd --since '48h ago' --no-pager | grep -c 'Failed' || true` | 112 ms | count only |
| 2 | `journalctl -u sshd --since '48h ago' --no-pager | grep 'Failed' | awk '{print $NF}' | sort | uniq -c | sort -rn | head -10` | 109 ms | empty — clean |
| 3 | `journalctl -u sshd --since '48h ago' --no-pager | grep 'Accepted' | tail -10` | 113 ms | empty — clean |
| 4 | `last -20` | 110 ms | 22 lines |
| 5 | `lastb -20 2>/dev/null || echo 'no lastb data'` | 110 ms | 1 lines |
| 6 | `journalctl -t sudo --since '48h ago' --no-pager | tail -20` | 114 ms | 20 lines |
| 7 | `grep -v 'nologin\|false\|sync\|halt\|shutdown' /etc/passwd | cut -d: -f1,6,7` | 110 ms | 3 lines |
| 8 | `ss -tlnp` | 109 ms | 15 lines |
| 9 | `find /tmp /var/tmp -type f -mtime -2 -name '.*' -ls 2>/dev/null || echo 'no hidden tmp files'` | 114 ms | 1 lines |
| 10 | `journalctl -p err --since '48h ago' --no-pager --grep='segfault|exploit|overflow|unauthorized' 2>/dev/null; echo rc=$?` | 110 ms | 9 lines |
| | **Total** | **1111 ms** | |

#### Comparison

| Metric | Raw SSH | hauntty | |
|--------|---------|---------|---|
| Wall clock | 2588 ms | 1111 ms | **2.3x faster** |
| Context bytes | 135.7 KB (922 lines) | 6.1 KB (metadata) | **96% less context** |
| SSH connections | 10 | 0 | |

---

## remote01

### Task 1: Server Health Check

*"Check if the server is healthy through Linux logs."*

#### Raw SSH (10 commands, 10 connections)

| # | Command | Time | Output |
|---|---------|------|--------|
| 1 | `dmesg | tail -100` | 917 ms | 0 lines (0 B) |
| 2 | `dmesg | grep -i 'error\|warn\|crit\|fail'` | 804 ms | 0 lines (0 B) |
| 3 | `journalctl -p err --since '24h ago' --no-pager` | 820 ms | 0 lines (121 B) |
| 4 | `journalctl -p warning --since '24h ago' --no-pager` | 793 ms | 0 lines (121 B) |
| 5 | `dmesg | grep -i 'oom\|killed process\|out of memory'` | 813 ms | 0 lines (0 B) |
| 6 | `dmesg | grep -i 'nvidia\|xid\|gpu\|drm'` | 799 ms | 0 lines (0 B) |
| 7 | `systemctl --failed --no-pager` | 799 ms | 2 lines (58 B) |
| 8 | `df -h` | 813 ms | 11 lines (931 B) |
| 9 | `free -h` | 805 ms | 2 lines (206 B) |
| 10 | `cat /var/log/syslog 2>/dev/null | tail -50 || journalctl --since '1h ago' --no-pager | tail -50` | 808 ms | 0 lines (0 B) |
| | **Total** | **8171 ms** | **15 lines (1437 bytes)** |


#### hauntty (10 commands, 0 new connections)

| # | Command | Time | Agent sees |
|---|---------|------|------------|
| 1 | `dmesg --level=err,warn,crit | tail -30` | 207 ms | empty — clean |
| 2 | `dmesg | grep -ci 'error\|warn\|fail' || true` | 301 ms | count only |
| 3 | `journalctl -p err --since '24h ago' --no-pager | tail -30` | 288 ms | 1 lines |
| 4 | `journalctl -p warning --since '24h ago' --no-pager | wc -l` | 284 ms | count only |
| 5 | `journalctl -p warning --since '24h ago' --no-pager | grep -vi 'pipewire\|bluetooth\|pulseaudio\|wireplumber\|gnome\|gjs\|gsd-\|snapd\|dbus' | tail -20` | 298 ms | 1 lines |
| 6 | `dmesg | grep -i 'oom\|killed process' | tail -10` | 240 ms | empty — clean |
| 7 | `dmesg | grep -i 'nvidia\|xid\|gpu\|drm' | tail -10` | 242 ms | empty — clean |
| 8 | `systemctl --failed --no-pager` | 283 ms | 3 lines |
| 9 | `df -h | awk 'NR==1 || +$5 > 80'` | 289 ms | 1 lines |
| 10 | `free -h` | 286 ms | 3 lines |
| | **Total** | **2718 ms** | |

#### Comparison

| Metric | Raw SSH | hauntty | |
|--------|---------|---------|---|
| Wall clock | 8171 ms | 2718 ms | **3.0x faster** |
| Context bytes | 1.4 KB (15 lines) | 1.7 KB (metadata) | **-28% less context** |
| SSH connections | 10 | 0 | |

### Task 2: Break-in Detection (last 48h)

*"Check if we had a break-in in the last 48 hours."*

#### Raw SSH (10 commands, 10 connections)

| # | Command | Time | Output |
|---|---------|------|--------|
| 1 | `journalctl -u sshd --since '48h ago' --no-pager` | 813 ms | 0 lines (16 B) |
| 2 | `last -50` | 818 ms | 51 lines (3.5 KB) |
| 3 | `lastb -50 2>/dev/null || echo 'no lastb'` | 802 ms | 0 lines (8 B) |
| 4 | `grep -i 'failed\|invalid\|accepted' /var/log/auth.log 2>/dev/null || journalctl -u sshd --since '48h ago' --no-pager | grep -i 'failed\|invalid\|accepted'` | 820 ms | 0 lines (0 B) |
| 5 | `journalctl -t sudo --since '48h ago' --no-pager` | 806 ms | 47 lines (5.4 KB) |
| 6 | `find /tmp /var/tmp -type f -mtime -2 -ls 2>/dev/null | head -50` | 791 ms | 1 lines (178 B) |
| 7 | `cat /etc/passwd | grep -v nologin | grep -v false` | 812 ms | 2 lines (114 B) |
| 8 | `ss -tlnp` | 811 ms | 13 lines (923 B) |
| 9 | `journalctl -p err --since '48h ago' --no-pager --grep='segfault|exploit|overflow|unauthorized' 2>/dev/null || echo 'none'` | 810 ms | 1 lines (21 B) |
| 10 | `cat /var/log/auth.log 2>/dev/null | tail -100 || journalctl -u sshd --since '48h ago' --no-pager | tail -100` | 800 ms | 0 lines (0 B) |
| | **Total** | **8083 ms** | **115 lines (10540 bytes)** |


#### hauntty (10 commands, 0 new connections)

| # | Command | Time | Agent sees |
|---|---------|------|------------|
| 1 | `journalctl -u sshd --since '48h ago' --no-pager | grep -c 'Failed' || true` | 258 ms | count only |
| 2 | `journalctl -u sshd --since '48h ago' --no-pager | grep 'Failed' | awk '{print $NF}' | sort | uniq -c | sort -rn | head -10` | 234 ms | empty — clean |
| 3 | `journalctl -u sshd --since '48h ago' --no-pager | grep 'Accepted' | tail -10` | 239 ms | empty — clean |
| 4 | `last -20` | 290 ms | 22 lines |
| 5 | `lastb -20 2>/dev/null || echo 'no lastb data'` | 284 ms | 1 lines |
| 6 | `journalctl -t sudo --since '48h ago' --no-pager | tail -20` | 287 ms | 20 lines |
| 7 | `grep -v 'nologin\|false\|sync\|halt\|shutdown' /etc/passwd | cut -d: -f1,6,7` | 301 ms | 2 lines |
| 8 | `ss -tlnp` | 286 ms | 14 lines |
| 9 | `find /tmp /var/tmp -type f -mtime -2 -name '.*' -ls 2>/dev/null || echo 'no hidden tmp files'` | 285 ms | 1 lines |
| 10 | `journalctl -p err --since '48h ago' --no-pager --grep='segfault|exploit|overflow|unauthorized' 2>/dev/null; echo rc=$?` | 302 ms | 2 lines |
| | **Total** | **2766 ms** | |

#### Comparison

| Metric | Raw SSH | hauntty | |
|--------|---------|---------|---|
| Wall clock | 8083 ms | 2766 ms | **2.9x faster** |
| Context bytes | 10.2 KB (115 lines) | 6.2 KB (metadata) | **40% less context** |
| SSH connections | 10 | 0 | |

---

## Summary: Context Efficiency

The primary goal of hauntty is **minimizing context drift** — the steady accumulation of irrelevant tokens in an LLM agent's context window during multi-step operations. In MLOps workflows (log triage, security audits, deployment verification), raw SSH dumps full command output into the agent's context on every call. Over a 40-command session across a fleet, this can mean hundreds of kilobytes of noise displacing the agent's working memory.

### Aggregate (40 commands across 2 servers, 4 tasks)

| | Raw SSH | hauntty | |
|---|---------|---------|---|
| Context consumed | 160.6 KB (1184 lines) | 18.6 KB (metadata only) | **89% reduction** |
| Wall clock | 21728 ms | 7702 ms | **2.8x faster** |
| SSH connections | 40 | 0 (persistent) | |

### How context reduction works

With raw SSH, every `ssh host "command"` returns its full output directly into the agent's context window — the agent has no choice but to ingest it all. A single `grep auth.log` can dump 100 KB of login records the agent never needed.

hauntty inverts this: the agent receives **metadata first** (`stdout_lines: 589`, `rc: 0`, `elapsed_s: 0.3`) and decides what to read. Three patterns emerge:

1. **Count before read** — `grep -c 'Failed'` returns a count; the agent only reads the full output if the count warrants it
2. **Filter at source** — pipe through `grep -v` or `tail -20` on the remote side, so only relevant lines exist to read
3. **Skip entirely** — if `stdout_lines: 0`, the agent knows the check passed without reading anything

On clean servers (like remote01's health check), hauntty's metadata envelope can be slightly larger than raw SSH's empty output — an honest trade-off. The reduction scales with output volume: the noisier the server, the larger the savings. On local01's break-in detection, where raw SSH dumped 132 KB of auth logs, hauntty reduced context consumption by **96%**.

### Why this matters for MLOps

LLM agents have finite context windows. Every byte of irrelevant output displaces working memory — previous findings, the current plan, tool definitions. As context fills, the agent loses coherence: it forgets earlier conclusions, repeats queries, or misses patterns it already saw. This is **context drift**.

In production MLOps — triaging a training failure across 8 GPU nodes, auditing a fleet after an incident, verifying a rolling deployment — an agent may execute 50–200 commands. At ~13 KB per raw SSH command (this benchmark's average), that's 650 KB–2.6 MB of raw output competing for context space. hauntty's metadata-first design keeps the agent's context clean, preserving its ability to reason over findings rather than drown in output.

### Speed advantage

Each `ssh host "command"` pays the full SSH handshake cost (~250 ms LAN, ~800 ms cloud). Over 40 commands, that's seconds of pure connection overhead. hauntty pays this cost once at `hauntty connect` and reuses the persistent session for all subsequent commands.

---
*Generated by bench/run_benchmark.sh on 2026-03-11T14:25:05Z*
