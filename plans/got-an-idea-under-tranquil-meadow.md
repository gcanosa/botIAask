# Plan: Humidity + Temperature Progress Bars

## Context

The `!weather` command currently shows temperature and humidity as plain text. The user wants:
1. A **humidity bar** with temperature-aware color coding (dewpoint-based semaphore)
2. A **temperature bar** showing where the current temp sits within today's min/max range, colored by heat level

---

## Part 1 — Humidity Bar (temperature-aware color)

### Science: dewpoint as the comfort signal

Simplified Magnus formula:
```
Td (°C) = T - ((100 - RH) / 5)
```

| Temp | Humidity | Dewpoint | Color |
|------|----------|----------|-------|
| 35°C | 60% | 27°C | 🔴 Red — oppressive |
| 24°C | 68% | 18°C | 🟠 Orange — muggy |
| 15°C | 68% | 9°C  | 🟢 Green — comfortable |

### Dewpoint color thresholds

| Dewpoint | mIRC | Feel |
|----------|------|------|
| < 10°C | `03` green | Dry / comfortable |
| 10–16°C | `08` yellow | Slightly humid |
| 16–21°C | `07` orange | Muggy |
| ≥ 21°C | `04` red | Oppressive |

### Output format

```
humidity [▓▓▓▓▓▓▓░░░] 68% dew 18°C
```

---

## Part 2 — Temperature Bar (current temp within today's range)

### Idea

Today's min/max already fetched (`s.Daily[0].MinC`, `s.Daily[0].MaxC`). Current temp is `s.Current.TempC`. We can show a bar where the needle sits within today's range.

```
percent = clamp((TempC - MinC) / (MaxC - MinC) * 100, 0, 100)
```

### Temperature color thresholds (heat scale)

| Temp | mIRC | Feel |
|------|------|------|
| < 0°C | `12` light-blue | Freezing |
| 0–10°C | `11` light-cyan | Cold |
| 10–20°C | `09` light-green | Cool/mild |
| 20–28°C | `08` yellow | Warm |
| 28–35°C | `07` orange | Hot |
| ≥ 35°C | `04` red | Very hot |

### Output format

Replace the bare `24°C` with a bar showing its position in today's range:

```
[▓▓▓▓▓▓░░░░] 24°C (18°–28°) Partly cloudy
```

The `(min°–max°)` suffix replaces the separate `· today: max°/min°` entry at the end of the line — they'd be redundant. We keep today's high/low only in the context of the bar.

---

## Full example: `!weather barcelona` (summer, 24°C, 68% humidity)

```
[WEATHER] Barcelona, ES — [▓▓▓▓▓▓░░░░] 24°C (18°–28°) Partly cloudy · wind 15 km/h · feels 26°C · humidity [▓▓▓▓▓▓▓░░░] 68% dew 18°C | 5d: Mon 27°/18° ...
```

Winter (12°C, 75% humidity):
```
[WEATHER] Barcelona, ES — [▓▓▓▓▓░░░░░] 12°C (9°–15°) Overcast · wind 20 km/h · humidity [▓▓▓▓▓▓▓▓░░] 75% dew 7°C | 5d: ...
```

---

## Implementation — only `irc/weather_cmd.go` changes

### Step 1 — add mIRC color constants

```go
// Semaphore / heat colors (add alongside existing ircMaxTemp, ircMinTemp)
ircGreen      = "\x03" + "03,01"  // green on black   — dewpoint comfortable
ircYellow     = "\x03" + "08,01"  // yellow on black  — dewpoint slightly muggy / warm
ircOrange     = "\x03" + "07,01"  // orange on black  — dewpoint muggy / hot
ircRed        = "\x03" + "04,01"  // red on black     — dewpoint oppressive / very hot
ircLightBlue  = "\x03" + "12,01"  // light-blue       — freezing
ircLightCyan  = "\x03" + "11,01"  // light-cyan       — cold
ircLightGreen = "\x03" + "09,01"  // light-green      — cool/mild
ircGray       = "\x03" + "14,01"  // gray on black    — bar track (empty)
```

### Step 2 — add `humidityBar` function

```go
func humidityBar(humidity int, tempC float64) string {
    const width = 10
    dew := tempC - float64(100-humidity)/5.0

    var color string
    switch {
    case dew < 10:
        color = ircGreen
    case dew < 16:
        color = ircYellow
    case dew < 21:
        color = ircOrange
    default:
        color = ircRed
    }

    filled := (humidity*width + 99) / 100
    bar := "[" + color + strings.Repeat("▓", filled) + ircEnd +
           ircGray + strings.Repeat("░", width-filled) + ircEnd + "]"

    return fmt.Sprintf("%s %d%% dew %d°C", bar, humidity, int(math.Round(dew)))
}
```

### Step 3 — add `tempBar` function

```go
func tempBar(tempC, minC, maxC float64) string {
    const width = 10
    span := maxC - minC
    var pct int
    if span > 0 {
        pct = int(math.Round((tempC - minC) / span * 100))
    }
    if pct < 0 { pct = 0 }
    if pct > 100 { pct = 100 }

    var color string
    switch {
    case tempC < 0:
        color = ircLightBlue
    case tempC < 10:
        color = ircLightCyan
    case tempC < 20:
        color = ircLightGreen
    case tempC < 28:
        color = ircYellow
    case tempC < 35:
        color = ircOrange
    default:
        color = ircRed
    }

    filled := (pct*width + 99) / 100
    bar := "[" + color + strings.Repeat("▓", filled) + ircEnd +
           ircGray + strings.Repeat("░", width-filled) + ircEnd + "]"

    return fmt.Sprintf("%s %.0f°C (%.0f°–%.0f°)", bar, tempC, minC, maxC)
}
```

### Step 4 — update `formatWeatherIRCLines`

**Current temp** (currently just `fmt.Sprintf("%.0f°C", s.Current.TempC)`):
```go
// Before:
parts = append(parts, fmt.Sprintf("%.0f°C", s.Current.TempC) + " " + s.Current.Summary)

// After (requires len(s.Daily) > 0):
if len(s.Daily) > 0 {
    parts = append(parts, tempBar(s.Current.TempC, s.Daily[0].MinC, s.Daily[0].MaxC)+" "+s.Current.Summary)
} else {
    parts = append(parts, fmt.Sprintf("%.0f°C", s.Current.TempC)+" "+s.Current.Summary)
}
```

**Remove `· today: max°/min°`** from the inline parts (it's now shown in the temp bar). The 5-day strip starting from `Mon` stays as-is.

**Humidity** (replace plain label):
```go
// Before:
parts = append(parts, ircBoldLabel("humidity")+" "+strconv.Itoa(s.Current.Humidity)+"%")

// After:
parts = append(parts, ircBoldLabel("humidity")+" "+humidityBar(s.Current.Humidity, s.Current.TempC))
```

---

## Files changed

| File | Change |
|------|--------|
| `irc/weather_cmd.go` | Add 8 color constants, add `humidityBar()`, add `tempBar()`, update 2 call sites, remove `today:` inline entry |

No changes to `weather/fetch.go` — all needed data (`Humidity`, `TempC`, `Daily[0].MinC/MaxC`) is already fetched.

---

## Verification

```bash
go build ./...

# In the bot:
!weather barcelona    # warm + muggy → orange bars
!weather reykjavik    # cold + humid → blue/green bars
!weather dubai        # hot + very humid → red bars
!weather sahara       # hot + dry → orange temp bar, green humidity bar
```

Check that IRC line stays under 450 bytes — each bar adds ~30 chars over old plain text. Total overhead ≈ +45 chars vs. current output. Should be fine; the 5-day strip already auto-splits to line 2 if needed.
